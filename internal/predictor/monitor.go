package predictor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const pollInterval = 15 * time.Second
const modelPointerPath = "models/intraday/current.json"
const trainRetryInterval = 5 * time.Minute
const actualGrace = 5 * time.Minute

var errNoReportToday = errors.New("no selection report for today")

// marketReader is the narrow read surface the predictor needs from market.db.
// storage.MarketDB satisfies it; tests use a small in-memory implementation.
type marketReader interface {
	LoadMinuteTrainingBars(context.Context, []string, time.Time, time.Time) (map[string][]market.MinuteBar, error)
	LoadDailyTrainingBars(context.Context, []string, string, string, string) (map[string][]market.Bar, error)
	LoadReport(context.Context, string) (selection.Report, error)
}

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

	reader           marketReader
	samplesBuilder   func(Data) ([]Sample, error)
	inferenceBuilder func(Data, string, time.Time) (FeatureVector, market.MinuteBar, error)
	trainer          func([]Sample, ModelInfo, TrainOptions) (*Artifact, error)
	walker           func([]Sample, ModelInfo, int, TrainOptions) (ModelMetrics, TargetMetrics, error)
	evaluator        func(*Model, []Sample) (ModelMetrics, TargetMetrics, error)

	workMu sync.Mutex

	mu               sync.Mutex
	model            *Model
	modelInfo        ModelInfo
	pushEnabled      bool
	status           Status
	ledger           notify.Ledger
	dailyCache       map[string][]market.Bar
	dailyCacheAt     string
	dailyCacheCodes  map[string]bool
	reportDate       string
	reportCache      selection.Report
	lastTrainAttempt time.Time
	lastAttemptEnd   time.Time
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
	m.applyDefaults()
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

func (m *Monitor) applyDefaults() {
	if m.Now == nil {
		m.Now = time.Now
	}
	if m.Log == nil {
		m.Log = logging.Nop()
	}
	if m.Calendar == nil {
		m.Calendar = calendar.New(m.Config)
	}
	if m.reader == nil {
		m.reader = m.Market
	}
	if m.samplesBuilder == nil {
		m.samplesBuilder = BuildTrainingSamples
	}
	if m.inferenceBuilder == nil {
		m.inferenceBuilder = BuildInference
	}
	if m.trainer == nil {
		m.trainer = Train
	}
	if m.walker == nil {
		m.walker = WalkForward
	}
	if m.evaluator == nil {
		m.evaluator = Evaluate
	}
	if m.dailyCache == nil {
		m.dailyCache = map[string][]market.Bar{}
	}
	if m.dailyCacheCodes == nil {
		m.dailyCacheCodes = map[string]bool{}
	}
	if m.ledger.Events == nil {
		m.ledger = notify.NewLedger()
	}
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
	m.applyDefaults()
	m.Log.Info("Predictor started", "train_at", m.Config.Predictor.TrainAt, "push_enabled", m.pushEnabled)
	for {
		now := m.Now()
		if m.trainingDue(now) {
			if _, err := m.Train(ctx, now); err != nil && ctx.Err() == nil {
				m.setError(err)
				m.Log.Warn("Predictor training failed", "error", err.Error())
			}
		}
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
	if m == nil {
		return Result{}, nil
	}
	m.applyDefaults()
	m.workMu.Lock()
	defer m.workMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now = now.In(market.Shanghai)
	date := calendar.Date(now)
	result := Result{Date: date}
	trading, err := m.Calendar.Trading(date)
	if err != nil {
		return result, err
	}
	if !trading {
		result.Skipped = []string{"not_trading_day"}
		return result, nil
	}
	if err := m.reconcile(ctx, date, now); err != nil {
		return result, err
	}
	slot := nextBarSlot(now)
	if !slot.OK {
		result.Skipped = []string{slot.Reason}
		return result, nil
	}
	result.TargetEnd = slot.TargetEnd

	m.mu.Lock()
	model := m.model
	info := m.modelInfo
	pushEnabled := m.pushEnabled
	m.mu.Unlock()
	if model == nil {
		result.Skipped = []string{"model_missing"}
		return result, nil
	}
	if info.ValidFor != date {
		result.Skipped = []string{"model_not_valid_for_today"}
		return result, nil
	}
	m.mu.Lock()
	attempted := m.lastAttemptEnd.Equal(slot.TargetEnd)
	m.mu.Unlock()
	if attempted {
		all, err := m.DB.PendingForTarget(ctx, date, slot.TargetEnd)
		if err != nil {
			return result, err
		}
		for _, prediction := range all {
			if prediction.ModelID == info.ModelID {
				result.Predictions = append(result.Predictions, prediction)
			}
		}
		result.Skipped = []string{"already_predicted"}
		return result, nil
	}

	report, err := m.reportForToday(ctx, date)
	if err != nil {
		if errors.Is(err, errNoReportToday) {
			result.Skipped = []string{"report_unavailable"}
			return result, nil
		}
		return result, err
	}
	selected, _ := reportUniverse(report, m.Config.Stocks, m.Config.Selection.TopN)
	if len(selected) == 0 {
		result.Skipped = []string{"no_selected_stocks"}
		return result, nil
	}
	universe := mergeStocks(m.Config.Stocks, selected)
	data, err := m.loadData(ctx, universe, now, false)
	if err != nil {
		return result, err
	}

	newPreds := make([]Prediction, 0, len(selected))
	skipped := []string{}
	for _, stock := range selected {
		features, input, err := m.inferenceBuilder(data, stock.Code, slot.InputEnd)
		if err != nil {
			skipped = append(skipped, stock.Code+":features_unavailable")
			continue
		}
		if input.Close <= 0 {
			skipped = append(skipped, stock.Code+":invalid_input_bar")
			continue
		}
		inferenceStart := time.Now()
		values, err := model.Predict(features.Values)
		inferenceMS := float64(time.Since(inferenceStart).Microseconds()) / 1000
		if err != nil {
			return result, err
		}
		kalman, err := m.kalmanFor(ctx, stock.Code, date, slot.InputEnd, data.Minutes[stock.Code])
		if err != nil {
			return result, err
		}
		trend, confidence, score := kalman.Trend()
		prediction := Prediction{
			Code: stock.Code, Date: date, InputEnd: slot.InputEnd, TargetEnd: slot.TargetEnd,
			ModelID: info.ModelID, BasePrice: input.Close,
			HighPct: values[TargetHigh], LowPct: values[TargetLow], ClosePct: values[TargetClose],
			Trend: trend, Confidence: confidence, TrendScore: score, CreatedAt: now.UTC(),
			InferenceMS: inferenceMS,
		}
		if err := prediction.Validate(); err != nil {
			skipped = append(skipped, stock.Code+":invalid_prediction")
			continue
		}
		newPreds = append(newPreds, prediction)
	}
	result.Skipped = skipped
	if len(newPreds) == 0 {
		return result, nil
	}

	inserted := 0
	for _, prediction := range newPreds {
		added, err := m.DB.InsertPrediction(ctx, prediction)
		if err != nil {
			return result, err
		}
		if added {
			inserted++
		}
	}
	all, err := m.DB.PendingForTarget(ctx, date, slot.TargetEnd)
	if err != nil {
		return result, err
	}
	latency, latErr := m.DB.LatencyStats(ctx, info.ModelID)
	if latErr != nil {
		m.Log.Warn("Predictor latency stats failed", "error", latErr.Error())
	}
	current := all[:0]
	for _, prediction := range all {
		if prediction.ModelID == info.ModelID {
			current = append(current, prediction)
		}
	}
	result.Predictions = current

	pushed := false
	if pushEnabled && len(current) > 0 {
		message, err := BuildCard(date, slot.TargetEnd, info, current)
		if err != nil {
			return result, err
		}
		channels := m.channelNames()
		if len(channels) > 0 {
			m.mu.Lock()
			_, exists := m.ledger.Events[message.EventID]
			if inserted > 0 || !exists {
				err = m.ledger.EnqueuePart(date, message, channels, "5m", 0, "")
				if err == nil {
					err = m.saveLedger()
				}
				pushed = err == nil
			}
			m.mu.Unlock()
			if err != nil {
				return result, err
			}
		}
	}
	result.Pushed = pushed
	if len(current) >= len(selected) {
		m.mu.Lock()
		m.lastAttemptEnd = slot.TargetEnd
		m.mu.Unlock()
	}

	m.mu.Lock()
	m.status.LastPredictionAt = now.UTC()
	m.status.LastPredictionEnd = slot.TargetEnd
	m.status.LastError = ""
	m.status.InferenceCount = latency.Count
	m.status.InferenceAvgMS = latency.AvgMS
	m.status.InferenceP95MS = latency.P95MS
	err = m.saveStatus()
	m.mu.Unlock()
	if err != nil {
		return result, err
	}
	if inserted > 0 {
		m.Log.Info("Predictor batch completed", "target_end", slot.TargetEnd.Format(time.RFC3339),
			"predictions", len(current), "inserted", inserted,
			"inference_count", latency.Count, "inference_avg_ms", latency.AvgMS, "inference_p95_ms", latency.P95MS)
	}
	if predictions, _, err := m.DB.Counts(ctx, date); err == nil {
		m.mu.Lock()
		m.status.PredictionsToday = predictions
		err = m.saveStatus()
		m.mu.Unlock()
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// Train fits the next valid model and writes it under the CIFS data root.
func (m *Monitor) Train(ctx context.Context, now time.Time) (ModelInfo, error) {
	if m == nil {
		return ModelInfo{}, errors.New("predictor is not configured")
	}
	m.applyDefaults()
	m.workMu.Lock()
	defer m.workMu.Unlock()
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return ModelInfo{}, err
	}
	now = now.In(market.Shanghai)
	if len(m.Config.Stocks) == 0 {
		return ModelInfo{}, errors.New("predictor requires a non-empty stock pool")
	}
	m.mu.Lock()
	m.lastTrainAttempt = now
	m.mu.Unlock()

	validFor, err := m.validFor(now)
	if err != nil {
		return ModelInfo{}, err
	}
	report, err := m.reportForToday(ctx, calendar.Date(now))
	if err != nil {
		if errors.Is(err, errNoReportToday) {
			return ModelInfo{}, errors.New("predictor training requires a selection report for today")
		}
		return ModelInfo{}, err
	}
	topN := report.TopN
	if topN <= 0 {
		topN = m.Config.Selection.TopN
	}
	universe, observed := reportUniverse(report, m.Config.Stocks, topN)
	if len(universe) == 0 {
		return ModelInfo{}, errors.New("predictor training requires selected or ranked stocks")
	}
	if observed {
		m.Log.Info("Predictor training uses observation ranking", "stocks", len(universe))
	}
	data, err := m.loadData(ctx, universe, now, true)
	if err != nil {
		return ModelInfo{}, err
	}
	samples, err := m.samplesBuilder(data)
	if err != nil {
		return ModelInfo{}, err
	}
	if now.Before(sessionClose(calendar.Date(now))) {
		// A model that becomes valid today must not learn from today's bars.
		// Mid-session training is only a startup fallback for a missing model.
		prior := make([]Sample, 0, len(samples))
		today := calendar.Date(now)
		for _, sample := range samples {
			if sample.Date < today {
				prior = append(prior, sample)
			}
		}
		samples = prior
	}
	if len(samples) == 0 {
		return ModelInfo{}, errors.New("predictor training requires finalized samples from prior sessions")
	}
	days := map[string]bool{}
	dataFrom, dataTo := "", ""
	for _, sample := range samples {
		day := sample.Date
		if day == "" {
			day = calendar.Date(sample.InputEnd)
		}
		if day == "" {
			continue
		}
		days[day] = true
		if dataFrom == "" || day < dataFrom {
			dataFrom = day
		}
		if dataTo == "" || day > dataTo {
			dataTo = day
		}
	}
	if len(days) < m.Config.Predictor.MinTrainingDays {
		return ModelInfo{}, fmt.Errorf("predictor training needs %d days, got %d", m.Config.Predictor.MinTrainingDays, len(days))
	}
	if len(samples[0].Features) != len(FeatureNames()) {
		return ModelInfo{}, fmt.Errorf("predictor feature count mismatch: got %d want %d", len(samples[0].Features), len(FeatureNames()))
	}
	info := ModelInfo{
		SchemaVersion:  ArtifactSchemaVersion,
		CreatedAt:      now.UTC(),
		DataFrom:       dataFrom,
		DataTo:         dataTo,
		ValidFor:       validFor,
		FeatureVersion: FeatureVersion,
		FeatureNames:   FeatureNames(),
		CodeVersion:    m.BuildVersion,
	}
	opts := DefaultTrainOptions()
	metrics, residuals, walkErr := m.walker(samples, info, m.Config.Predictor.ValidationDays, opts)
	if walkErr != nil {
		m.Log.Warn("Predictor walk-forward failed", "error", walkErr.Error())
	}
	artifact, err := m.trainer(samples, info, opts)
	if err != nil {
		return ModelInfo{}, err
	}
	if artifact == nil {
		return ModelInfo{}, errors.New("predictor trainer returned no artifact")
	}
	artifact.Info.SchemaVersion = ArtifactSchemaVersion
	artifact.Info.CreatedAt = now.UTC()
	artifact.Info.DataFrom = dataFrom
	artifact.Info.DataTo = dataTo
	artifact.Info.ValidFor = validFor
	artifact.Info.FeatureVersion = FeatureVersion
	artifact.Info.FeatureNames = FeatureNames()
	artifact.Info.CodeVersion = m.BuildVersion
	artifact.Info.Metrics = metrics
	artifact.Info.Residuals = residuals
	if walkErr != nil {
		if evaluated, bands, evalErr := m.evaluator(&Model{Artifact: *artifact}, samples); evalErr == nil {
			metrics, residuals = evaluated, bands
			artifact.Info.Metrics = metrics
			artifact.Info.Residuals = residuals
		}
	}
	if artifact.Info.Metrics.Samples == 0 {
		artifact.Info.Metrics.Samples = len(samples)
		artifact.Info.Metrics.Days = len(days)
	}
	artifact.Info.TrainDurationMS = time.Since(started).Milliseconds()
	sealed, err := SealArtifact(artifact)
	if err != nil {
		return ModelInfo{}, err
	}
	modelPath := fmt.Sprintf("models/intraday/%s/model.json", artifact.Info.ModelID)
	metadataPath := fmt.Sprintf("models/intraday/%s/metadata.json", artifact.Info.ModelID)
	info = artifact.Info
	info.ArtifactPath = modelPath
	if err := m.Store.Write(modelPath, json.RawMessage(sealed)); err != nil {
		return ModelInfo{}, err
	}
	if err := m.Store.Write(metadataPath, info); err != nil {
		return ModelInfo{}, err
	}
	if err := m.DB.RecordModel(ctx, info); err != nil {
		return ModelInfo{}, err
	}
	pointer := ModelPointer{SchemaVersion: 1, ModelID: info.ModelID, Path: modelPath,
		Checksum: info.Checksum, ValidFor: info.ValidFor, CreatedAt: info.CreatedAt}
	if err := m.Store.Write(modelPointerPath, pointer); err != nil {
		return ModelInfo{}, err
	}
	m.pruneModels(info.ModelID)
	model, err := LoadModel(sealed)
	if err != nil {
		return ModelInfo{}, err
	}
	m.mu.Lock()
	m.model = model
	m.modelInfo = info
	m.lastAttemptEnd = time.Time{}
	m.status.ModelID = info.ModelID
	m.status.ModelValidFor = info.ValidFor
	m.status.TrainedAt = info.CreatedAt
	m.status.TrainDurationMS = info.TrainDurationMS
	m.status.LastError = ""
	err = m.saveStatus()
	m.mu.Unlock()
	if err != nil {
		return ModelInfo{}, err
	}
	m.Log.Info("Predictor model trained", "model_id", info.ModelID, "valid_for", info.ValidFor,
		"samples", info.Metrics.Samples, "days", len(days), "train_ms", info.TrainDurationMS)
	return info, nil
}

func (m *Monitor) pruneModels(current string) {
	keep := m.Config.Predictor.KeepModels
	if keep <= 0 || m.Config.DataDir == "" {
		return
	}
	root := filepath.Join(m.Config.DataDir, "models", "intraday")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	others := []string{}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != current {
			others = append(others, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(others)))
	maxOthers := keep - 1
	for index, name := range others {
		if index < maxOthers {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			m.Log.Warn("Predictor model prune failed", "model", name, "error", err.Error())
		}
	}
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
	if err := WriteRuntimeConfig(m.Store, RuntimeConfig{PushEnabled: enabled}); err != nil {
		return err
	}
	m.pushEnabled = enabled
	m.status.PushEnabled = enabled
	m.status.UpdatedAt = m.Now().UTC()
	if !enabled {
		if m.expirePendingLocked() {
			_ = m.saveLedger()
		}
	}
	return m.saveStatus()
}

func (m *Monitor) trainingDue(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.lastTrainAttempt.IsZero() && now.Sub(m.lastTrainAttempt) < trainRetryInterval {
		return false
	}
	today := calendar.Date(now)
	closeAt := sessionClose(today)
	if m.model == nil {
		if now.Before(closeAt) {
			return true
		}
		at, err := m.trainAt(now)
		return err != nil || !now.Before(at)
	}
	if m.model.Artifact.Info.ValidFor != today && now.Before(closeAt) {
		return true
	}
	if m.model.Artifact.Info.ValidFor == today && now.Before(closeAt) {
		return false
	}
	at, err := m.trainAt(now)
	if err != nil || now.Before(at) {
		return false
	}
	next, err := m.Calendar.Next(today)
	if err != nil {
		return false
	}
	return m.model.Artifact.Info.ValidFor != next
}

func (m *Monitor) trainAt(now time.Time) (time.Time, error) {
	clock := m.Config.Predictor.TrainAt
	parsed, err := time.ParseInLocation("15:04", clock, market.Shanghai)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid predictor train_at %q", clock)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, market.Shanghai), nil
}

func (m *Monitor) validFor(now time.Time) (string, error) {
	today := calendar.Date(now)
	if now.Before(sessionClose(today)) {
		return today, nil
	}
	return m.Calendar.Next(today)
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
	if model.Artifact.Info.ModelID != pointer.ModelID {
		return errors.New("predictor model pointer does not match artifact")
	}
	m.model = model
	m.modelInfo = model.Artifact.Info
	m.status.ModelID = model.Artifact.Info.ModelID
	m.status.ModelValidFor = model.Artifact.Info.ValidFor
	m.status.TrainedAt = model.Artifact.Info.CreatedAt
	m.status.TrainDurationMS = model.Artifact.Info.TrainDurationMS
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
	if m.ledger.Normalize(m.Now()) {
		if err := m.saveLedger(); err != nil {
			return err
		}
	}
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
		if m.expirePendingLocked() {
			return m.saveLedger()
		}
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
		if err := m.Store.Check(); err != nil {
			return err
		}
		if err := m.ledger.MarkInFlight(delivery.EventID, delivery.Channel, now); err != nil {
			continue
		}
		if err := m.saveLedger(); err != nil {
			return err
		}
		remaining := delivery.Message.ExpiresAt.Sub(m.Now())
		if remaining <= 0 {
			m.ledger.Expire(m.Now())
			continue
		}
		timeout := 10 * time.Second
		if remaining < timeout {
			timeout = remaining
		}
		sendCtx, cancel := context.WithTimeout(ctx, timeout)
		err := channel.Send(sendCtx, delivery.Message)
		cancel()
		if err == nil {
			_ = m.ledger.MarkDelivered(delivery.EventID, delivery.Channel)
		} else {
			failure := &notify.DeliveryError{Kind: notify.Retryable, Reason: "send_failed"}
			if classified, ok := err.(*notify.DeliveryError); ok {
				failure = classified
			}
			_ = m.ledger.MarkFailure(delivery.EventID, delivery.Channel, failure, m.Now())
		}
		if err := m.saveLedger(); err != nil {
			return err
		}
	}
	m.ledger.Expire(now)
	m.ledger.Prune(now, 30*24*time.Hour)
	m.status.PushDelivered, m.status.PushBlocked = m.deliveryCounts(calendar.Date(now))
	return m.saveLedger()
}

func (m *Monitor) expirePendingLocked() bool {
	changed := false
	for id, event := range m.ledger.Events {
		for channel, state := range event.Channels {
			switch state.Status {
			case notify.Pending, notify.InFlight, notify.Unknowned:
				state.Status = notify.Expired
				state.NextAttempt = time.Time{}
				state.InFlightAt = time.Time{}
				event.Channels[channel] = state
				changed = true
			}
		}
		m.ledger.Events[id] = event
	}
	return changed
}

func (m *Monitor) deliveryCounts(asof string) (delivered, blocked int) {
	for _, event := range m.ledger.Events {
		if event.AsOf != asof {
			continue
		}
		for _, state := range event.Channels {
			switch state.Status {
			case notify.Delivered:
				delivered++
			case notify.Blocked:
				blocked++
			}
		}
	}
	return delivered, blocked
}

type barSlot struct {
	Date      string
	InputEnd  time.Time
	TargetEnd time.Time
	OK        bool
	Reason    string
}

// nextBarSlot returns the latest finalized five-minute bar and the next native
// bar it predicts. Lunch and the closing bar never produce a target.
func nextBarSlot(now time.Time) barSlot {
	t := now.In(market.Shanghai)
	date := t.Format("2006-01-02")
	y, month, day := t.Date()
	at := func(hour, minute int) time.Time {
		return time.Date(y, month, day, hour, minute, 0, 0, market.Shanghai)
	}
	minutes := t.Hour()*60 + t.Minute()
	switch {
	case minutes < 9*60+35:
		return barSlot{Date: date, Reason: "before_open"}
	case minutes >= 15*60:
		return barSlot{Date: date, Reason: "after_close"}
	case minutes >= 13*60+5:
		offset := ((minutes - 13*60) / 5) * 5
		if offset > 120 {
			offset = 120
		}
		input := at(13, offset)
		return barSlot{Date: date, InputEnd: input, TargetEnd: input.Add(5 * time.Minute), OK: true}
	case minutes >= 11*60+30:
		return barSlot{Date: date, Reason: "lunch_break"}
	default:
		offset := ((minutes - (9*60 + 30)) / 5) * 5
		if offset > 120 {
			offset = 120
		}
		input := at(9, 30+offset)
		return barSlot{Date: date, InputEnd: input, TargetEnd: input.Add(5 * time.Minute), OK: true}
	}
}

func sessionClose(date string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", date, market.Shanghai)
	if err != nil {
		return time.Time{}
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 15, 0, 0, 0, market.Shanghai)
}

func selectedStocks(report selection.Report, pool []config.Stock) []config.Stock {
	byCode := map[string]config.Stock{}
	for _, stock := range pool {
		byCode[stock.Code] = stock
	}
	out := make([]config.Stock, 0, len(report.Selected))
	seen := map[string]bool{}
	for _, candidate := range report.Selected {
		if candidate.Code == "" || seen[candidate.Code] {
			continue
		}
		seen[candidate.Code] = true
		stock, ok := byCode[candidate.Code]
		if !ok {
			stock = config.Stock{Code: candidate.Code, Name: candidate.Name, Sector: candidate.Sector}
		}
		out = append(out, stock)
	}
	return out
}

// reportUniverse prefers the formal selection and falls back to the top-N
// observation ranking when strict selection produced no candidates.
func reportUniverse(report selection.Report, pool []config.Stock, topN int) ([]config.Stock, bool) {
	if selected := selectedStocks(report, pool); len(selected) > 0 {
		return selected, false
	}
	if topN <= 0 {
		topN = len(report.Ranking)
	}
	byCode := map[string]config.Stock{}
	for _, stock := range pool {
		byCode[stock.Code] = stock
	}
	out := make([]config.Stock, 0, topN)
	seen := map[string]bool{}
	for _, candidate := range report.Ranking {
		if len(out) >= topN {
			break
		}
		if candidate.Code == "" || seen[candidate.Code] {
			continue
		}
		seen[candidate.Code] = true
		stock, ok := byCode[candidate.Code]
		if !ok {
			stock = config.Stock{Code: candidate.Code, Name: candidate.Name}
		}
		out = append(out, stock)
	}
	return out, true
}

func mergeStocks(pool, extra []config.Stock) []config.Stock {
	out := append([]config.Stock(nil), pool...)
	seen := map[string]bool{}
	for _, stock := range out {
		seen[stock.Code] = true
	}
	for _, stock := range extra {
		if !seen[stock.Code] {
			seen[stock.Code] = true
			out = append(out, stock)
		}
	}
	return out
}

func (m *Monitor) reportForToday(ctx context.Context, date string) (selection.Report, error) {
	m.mu.Lock()
	if m.reportDate == date {
		report := m.reportCache
		m.mu.Unlock()
		return report, nil
	}
	m.mu.Unlock()
	start, err := time.ParseInLocation("2006-01-02", date, market.Shanghai)
	if err != nil {
		return selection.Report{}, err
	}
	for i := 0; i < 15; i++ {
		asof := calendar.Date(start.AddDate(0, 0, -i))
		report, err := m.reader.LoadReport(ctx, asof)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return selection.Report{}, err
		}
		if report.Target == date {
			m.mu.Lock()
			m.reportDate = date
			m.reportCache = report
			m.mu.Unlock()
			return report, nil
		}
	}
	return selection.Report{}, errNoReportToday
}

func (m *Monitor) loadData(ctx context.Context, stocks []config.Stock, now time.Time, training bool) (Data, error) {
	codes := make([]string, 0, len(stocks))
	for _, stock := range stocks {
		if stock.Code != "" {
			codes = append(codes, stock.Code)
		}
	}
	if len(codes) == 0 {
		return Data{}, errors.New("predictor requires at least one stock")
	}
	days := 3
	if training {
		days = 400
	}
	minutes, err := m.reader.LoadMinuteTrainingBars(ctx, codes, now.AddDate(0, 0, -days), now)
	if err != nil {
		return Data{}, err
	}
	daily, err := m.loadDaily(ctx, codes, calendar.Date(now), training)
	if err != nil {
		return Data{}, err
	}
	return Data{Stocks: stocks, Minutes: minutes, Daily: daily}, nil
}

func (m *Monitor) loadDaily(ctx context.Context, codes []string, through string, force bool) (map[string][]market.Bar, error) {
	if !force {
		m.mu.Lock()
		if m.dailyCacheAt == through {
			complete := true
			for _, code := range codes {
				if !m.dailyCacheCodes[code] {
					complete = false
					break
				}
			}
			if complete {
				cached := make(map[string][]market.Bar, len(m.dailyCache))
				for code, bars := range m.dailyCache {
					cached[code] = bars
				}
				m.mu.Unlock()
				return cached, nil
			}
		}
		m.mu.Unlock()
	}
	t, err := time.ParseInLocation("2006-01-02", through, market.Shanghai)
	if err != nil {
		return nil, err
	}
	from := calendar.Date(t.AddDate(-2, 0, 0))
	daily, err := m.reader.LoadDailyTrainingBars(ctx, codes, from, through, m.Config.Daily.Adjust)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.dailyCache = daily
	m.dailyCacheAt = through
	m.dailyCacheCodes = map[string]bool{}
	for _, code := range codes {
		m.dailyCacheCodes[code] = true
	}
	m.mu.Unlock()
	return daily, nil
}

func (m *Monitor) kalmanFor(ctx context.Context, code, date string, inputEnd time.Time, bars []market.MinuteBar) (*Kalman, error) {
	state, err := m.DB.Kalman(ctx, code, date)
	if err != nil {
		return nil, err
	}
	kalman := NewKalman(DefaultKalmanParams())
	if state.Code != "" {
		kalman = KalmanFromState(state, DefaultKalmanParams())
	}
	dayStart, err := time.ParseInLocation("2006-01-02", date, market.Shanghai)
	if err != nil {
		return nil, err
	}
	ordered := append([]market.MinuteBar(nil), bars...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].End.Before(ordered[j].End) })
	for _, bar := range ordered {
		if !bar.Final || !bar.End.After(kalman.State.BarEnd) {
			continue
		}
		if !kalman.Initialized && bar.End.Before(dayStart) {
			continue
		}
		if bar.End.After(inputEnd) {
			continue
		}
		kalman.Update(code, date, bar.End, bar.Close)
	}
	if kalman.Initialized {
		if err := m.DB.SaveKalman(ctx, kalman.State); err != nil {
			return nil, err
		}
	}
	return kalman, nil
}

type actualKey struct {
	code string
	end  int64
}

func (m *Monitor) actualStatuses(ctx context.Context, from, through time.Time) (map[actualKey]string, error) {
	out := map[actualKey]string{}
	rows, err := m.DB.db.QueryContext(ctx, `SELECT code,target_end_unix,status FROM prediction_actual
		WHERE target_end_unix>=? AND target_end_unix<=?`, from.Unix(), through.Unix())
	if err != nil {
		return nil, errors.New("cannot read predictor actuals")
	}
	defer rows.Close()
	for rows.Next() {
		var key actualKey
		var status string
		if err := rows.Scan(&key.code, &key.end, &status); err != nil {
			return nil, errors.New("cannot decode predictor actuals")
		}
		out[key] = status
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cannot read predictor actuals")
	}
	return out, nil
}

func (m *Monitor) reconcile(ctx context.Context, date string, now time.Time) error {
	predictions, err := m.DB.ForDate(ctx, date)
	if err != nil || len(predictions) == 0 {
		return err
	}
	codes := make([]string, 0, len(predictions))
	seen := map[string]bool{}
	for _, prediction := range predictions {
		if !seen[prediction.Code] {
			seen[prediction.Code] = true
			codes = append(codes, prediction.Code)
		}
	}
	start, err := time.ParseInLocation("2006-01-02", date, market.Shanghai)
	if err != nil {
		return err
	}
	minutes, err := m.reader.LoadMinuteTrainingBars(ctx, codes, start, now.Add(time.Second))
	if err != nil {
		return err
	}
	bars := map[actualKey]market.MinuteBar{}
	for code, list := range minutes {
		for _, bar := range list {
			if bar.Final {
				bars[actualKey{code: code, end: bar.End.Unix()}] = bar
			}
		}
	}
	statuses, err := m.actualStatuses(ctx, start, now.Add(time.Second))
	if err != nil {
		return err
	}
	eod := !now.Before(sessionClose(date).Add(actualGrace))
	for _, prediction := range predictions {
		if prediction.TargetEnd.After(now) || prediction.BasePrice <= 0 {
			continue
		}
		key := actualKey{code: prediction.Code, end: prediction.TargetEnd.Unix()}
		if statuses[key] == "resolved" {
			continue
		}
		actual := Actual{Code: prediction.Code, TargetEnd: prediction.TargetEnd, ModelID: prediction.ModelID,
			Status: "unresolved", ResolvedAt: now.UTC()}
		if bar, ok := bars[key]; ok {
			base := prediction.BasePrice
			actual.HighPct = (bar.High - base) / base
			actual.LowPct = (bar.Low - base) / base
			actual.ClosePct = (bar.Close - base) / base
			actual.Status = "resolved"
		} else if !eod {
			continue
		} else if statuses[key] == "unresolved" {
			continue
		}
		if err := m.DB.InsertActual(ctx, actual); err != nil {
			return err
		}
	}
	return nil
}
