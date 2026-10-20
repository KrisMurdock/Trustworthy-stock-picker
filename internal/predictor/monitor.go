package predictor

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const pollInterval = 15 * time.Second
const modelPointerPath = "models/intraday/current.json"

type Monitor struct {
	Config       config.Config
	Store        *storage.Store
	Market       *storage.MarketDB
	Channels     []notify.Channel
	Log          *logging.Logger
	Now          func() time.Time
	BuildVersion string
	Calendar     *calendar.Calendar
	DB           *DB

	mu           sync.Mutex
	model        *Model
	modelInfo    ModelInfo
	pushEnabled  bool
	status       Status
	ledger       notify.Ledger
	dailyCache   map[string][]market.Bar
	dailyCacheAt string
}

func New(c config.Config, store *storage.Store, marketDB *storage.MarketDB, channels []notify.Channel, log *logging.Logger) (*Monitor, error) {
	if !c.Predictor.Enabled {
		return nil, nil
	}
	if store == nil || marketDB == nil {
		return nil, errors.New("predictor requires writable storage and market database")
	}
	if log == nil {
		log = logging.Nop()
	}
	db, err := OpenDB(c.DataDir)
	if err != nil {
		return nil, err
	}
	m := &Monitor{
		Config: c, Store: store, Market: marketDB, Channels: channels, Log: log,
		Now: time.Now, BuildVersion: "development", Calendar: calendar.New(c), DB: db,
		pushEnabled: c.Predictor.PushEnabled, dailyCache: map[string][]market.Bar{},
		status: Status{Version: StatusSchemaVersion, Enabled: true, PushEnabled: c.Predictor.PushEnabled},
	}
	if runtimeCfg, err := ReadRuntimeConfig(store); err == nil && !runtimeCfg.UpdatedAt.IsZero() {
		m.pushEnabled = runtimeCfg.PushEnabled
		m.status.PushEnabled = runtimeCfg.PushEnabled
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		db.Close()
		return nil, err
	}
	if err := m.loadModel(); err != nil {
		m.Log.Warn("Predictor model unavailable", "error", err.Error())
	}
	if err := m.loadLedger(); err != nil {
		db.Close()
		return nil, err
	}
	return m, nil
}

func (m *Monitor) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Store != nil && m.DB != nil {
		_ = m.saveStatus()
		_ = m.saveLedger()
	}
	if m.DB != nil {
		return m.DB.Close()
	}
	return nil
}

func (m *Monitor) Run(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.Log.Info("Predictor started", "train_at", m.Config.Predictor.TrainAt, "push_enabled", m.pushEnabled)
	if m.model == nil {
		if _, err := m.Train(ctx, m.Now()); err != nil {
			m.setError(err)
			m.Log.Warn("Predictor startup training failed", "error", err.Error())
		}
	}
	for {
		now := m.Now()
		if _, err := m.PredictOnce(ctx, now); err != nil && ctx.Err() == nil {
			m.setError(err)
			m.Log.Warn("Predictor cycle failed", "error", err.Error())
		}
		if err := m.flush(ctx, now); err != nil && ctx.Err() == nil {
			m.Log.Warn("Predictor delivery failed", "error", err.Error())
		}
		m.mu.Lock()
		_ = m.saveStatus()
		m.mu.Unlock()
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// PredictOnce performs at most one prediction batch for the current target bar.
func (m *Monitor) PredictOnce(ctx context.Context, now time.Time) (Result, error) {
	return Result{}, errors.New("predictor cycle is not implemented")
}

// Train fits the next valid model and writes it under the CIFS data root.
func (m *Monitor) Train(ctx context.Context, now time.Time) (ModelInfo, error) {
	return ModelInfo{}, errors.New("predictor training is not implemented")
}

func (m *Monitor) Status(ctx context.Context) (Status, error) {
	if m == nil {
		return Status{Version: StatusSchemaVersion}, nil
	}
	status, err := ReadStatus(m.Store)
	if err == nil {
		return status, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Version = StatusSchemaVersion
	m.status.Enabled = true
	m.status.PushEnabled = m.pushEnabled
	return m.status, nil
}

func (m *Monitor) View(ctx context.Context) (View, error) {
	status, err := m.Status(ctx)
	if err != nil {
		return View{}, err
	}
	return View{Status: status, WriteEnabled: true, WriteMode: m.Config.Predictor.WriteMode}, nil
}

// History returns recorded predictions for one trading date.
func (m *Monitor) History(ctx context.Context, date string) ([]Prediction, error) {
	if m == nil || m.DB == nil {
		return []Prediction{}, nil
	}
	return m.DB.ForDate(ctx, date)
}

// ModelInfo returns the recorded metadata for a model ID.
func (m *Monitor) ModelInfo(ctx context.Context, modelID string) (ModelInfo, error) {
	if m == nil || m.DB == nil {
		return ModelInfo{}, errors.New("predictor is not configured")
	}
	return m.DB.ModelInfo(ctx, modelID)
}

func (m *Monitor) SetPushEnabled(ctx context.Context, enabled bool) error {
	if m == nil {
		return errors.New("predictor is not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushEnabled = enabled
	m.status.PushEnabled = enabled
	m.status.UpdatedAt = m.Now().UTC()
	if err := WriteRuntimeConfig(m.Store, RuntimeConfig{PushEnabled: enabled}); err != nil {
		return err
	}
	if !enabled {
		now := m.Now()
		for id, event := range m.ledger.Events {
			event.ExpiresAt = now
			m.ledger.Events[id] = event
		}
		m.ledger.Expire(now)
		_ = m.saveLedger()
	}
	return m.saveStatus()
}

func (m *Monitor) loadModel() error {
	var pointer ModelPointer
	if err := m.Store.Read(modelPointerPath, &pointer); err != nil {
		return err
	}
	if pointer.SchemaVersion != 1 || pointer.Path == "" || pointer.ModelID == "" {
		return errors.New("invalid predictor model pointer")
	}
	var artifact Artifact
	if err := m.Store.Read(pointer.Path, &artifact); err != nil {
		return err
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	model, err := LoadModel(raw)
	if err != nil {
		return err
	}
	m.model = model
	m.modelInfo = model.Artifact.Info
	m.status.ModelID = model.Artifact.Info.ModelID
	m.status.ModelValidFor = model.Artifact.Info.ValidFor
	m.status.TrainedAt = model.Artifact.Info.CreatedAt
	return nil
}

func (m *Monitor) loadLedger() error {
	var ledger notify.Ledger
	err := m.Store.Read("state/predictor-delivery.json", &ledger)
	if errors.Is(err, fs.ErrNotExist) {
		m.ledger = notify.NewLedger()
		return nil
	}
	if err != nil {
		return err
	}
	if ledger.Version != 2 {
		return errors.New("unsupported predictor delivery ledger")
	}
	if err := ledger.Validate(); err != nil {
		return err
	}
	m.ledger = ledger
	return nil
}

func (m *Monitor) saveLedger() error {
	return m.Store.Write("state/predictor-delivery.json", m.ledger)
}

func (m *Monitor) saveStatus() error {
	m.status.Version = StatusSchemaVersion
	m.status.Enabled = true
	m.status.PushEnabled = m.pushEnabled
	m.status.UpdatedAt = m.Now().UTC()
	return WriteStatus(m.Store, m.status)
}

func (m *Monitor) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.status.LastError = err.Error()
	}
}

func (m *Monitor) channelNames() []string {
	out := make([]string, 0, len(m.Channels))
	for _, channel := range m.Channels {
		if channel != nil && channel.Name() != "" {
			out = append(out, channel.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (m *Monitor) flush(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.pushEnabled {
		return nil
	}
	channels := map[string]notify.Channel{}
	for _, channel := range m.Channels {
		if channel != nil {
			channels[channel.Name()] = channel
		}
	}
	for _, delivery := range m.ledger.Due(now) {
		channel, ok := channels[delivery.Channel]
		if !ok {
			_ = m.ledger.MarkFailure(delivery.EventID, delivery.Channel, &notify.DeliveryError{Kind: notify.Permanent, Reason: "channel_missing"}, now)
			continue
		}
		if !m.ledger.CanDeliver(delivery.EventID, delivery.Channel) {
			continue
		}
		if err := m.ledger.MarkInFlight(delivery.EventID, delivery.Channel, now); err != nil {
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := channel.Send(sendCtx, delivery.Message)
		cancel()
		if err == nil {
			_ = m.ledger.MarkDelivered(delivery.EventID, delivery.Channel)
			continue
		}
		failure := &notify.DeliveryError{Kind: notify.Retryable, Reason: "send_failed"}
		if classified, ok := err.(*notify.DeliveryError); ok {
			failure = classified
		}
		_ = m.ledger.MarkFailure(delivery.EventID, delivery.Channel, failure, now)
	}
	m.ledger.Expire(now)
	return m.saveLedger()
}
