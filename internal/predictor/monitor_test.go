package predictor

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type monitorFakeReader struct {
	reports map[string]selection.Report
	minutes map[string][]market.MinuteBar
	daily   map[string][]market.Bar
}

func (f *monitorFakeReader) LoadReport(_ context.Context, asof string) (selection.Report, error) {
	if report, ok := f.reports[asof]; ok {
		return report, nil
	}
	return selection.Report{}, fs.ErrNotExist
}

func (f *monitorFakeReader) LoadMinuteTrainingBars(_ context.Context, codes []string, from, to time.Time) (map[string][]market.MinuteBar, error) {
	out := map[string][]market.MinuteBar{}
	for _, code := range codes {
		for _, bar := range f.minutes[code] {
			if !bar.End.Before(from) && bar.End.Before(to) {
				out[code] = append(out[code], bar)
			}
		}
	}
	return out, nil
}

func (f *monitorFakeReader) LoadDailyTrainingBars(_ context.Context, codes []string, from, through, _ string) (map[string][]market.Bar, error) {
	out := map[string][]market.Bar{}
	for _, code := range codes {
		for _, bar := range f.daily[code] {
			if bar.Date >= from && bar.Date <= through {
				out[code] = append(out[code], bar)
			}
		}
	}
	return out, nil
}

type monitorFakeChannel struct {
	name string
	sent []notify.Message
}

func (f *monitorFakeChannel) Name() string { return f.name }

func (f *monitorFakeChannel) Send(_ context.Context, message notify.Message) error {
	f.sent = append(f.sent, message)
	return nil
}

func monitorTestConfig(root string) config.Config {
	c := config.Defaults()
	c.DataDir = root
	c.RequireCIFS = false
	c.Daily.Adjust = "qfq"
	c.Predictor = config.DefaultPredictor()
	c.Predictor.Enabled = true
	c.Predictor.PushEnabled = true
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Stocks = []config.Stock{{Code: "sh600000", Name: "Test"}}
	return c
}

func monitorNewTestMonitor(t *testing.T, now time.Time) (*Monitor, *monitorFakeReader, *monitorFakeChannel) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.Open(root, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	marketDB, err := storage.InitMarket(root, false)
	if err != nil {
		store.Close()
		t.Fatalf("init market: %v", err)
	}
	db, err := OpenDB(root)
	if err != nil {
		marketDB.Close()
		store.Close()
		t.Fatalf("open predictor db: %v", err)
	}
	c := monitorTestConfig(root)
	reader := &monitorFakeReader{reports: map[string]selection.Report{}, minutes: map[string][]market.MinuteBar{}, daily: map[string][]market.Bar{}}
	channel := &monitorFakeChannel{name: "fake"}
	m := &Monitor{
		Config: c, Store: store, Market: marketDB, DB: db,
		Channels: []notify.Channel{channel}, Log: logging.Nop(),
		Now: func() time.Time { return now }, BuildVersion: "test", Calendar: calendar.New(c),
		pushEnabled: true, status: Status{Version: StatusSchemaVersion, Enabled: true, PushEnabled: true},
		ledger: notify.NewLedger(),
	}
	m.applyDefaults()
	m.reader = reader
	m.inferenceBuilder = func(Data, string, time.Time) (FeatureVector, market.MinuteBar, error) {
		return FeatureVector{Names: FeatureNames(), Values: make([]float64, len(FeatureNames()))},
			market.MinuteBar{Close: 10, Final: true}, nil
	}
	t.Cleanup(func() {
		db.Close()
		marketDB.Close()
		store.Close()
	})
	return m, reader, channel
}

func monitorTestModel(validFor string) *Model {
	opts := DefaultTrainOptions()
	info := ModelInfo{
		SchemaVersion: ArtifactSchemaVersion, ModelID: "model-" + validFor,
		CreatedAt: time.Date(2026, 10, 20, 15, 30, 0, 0, market.Shanghai), ValidFor: validFor,
		FeatureVersion: FeatureVersion, FeatureNames: FeatureNames(),
		Metrics: ModelMetrics{Samples: 10000, Days: 100, MAE: TargetMetrics{High: 0.01, Low: 0.01, Close: 0.01},
			BaselineLastMAE: TargetMetrics{High: 0.02, Low: 0.02, Close: 0.02}},
	}
	return &Model{Artifact: Artifact{
		SchemaVersion: ArtifactSchemaVersion, Info: info, Hyperparameters: opts.Hyperparameters,
		Base:      map[Target]float64{TargetHigh: 0.01, TargetLow: -0.01, TargetClose: 0},
		Trees:     map[Target][]Tree{TargetHigh: {}, TargetLow: {}, TargetClose: {}},
		LabelClip: map[Target]float64{TargetHigh: 0.05, TargetLow: 0.05, TargetClose: 0.05},
	}}
}

func monitorSetModel(m *Monitor, validFor string) {
	m.model = monitorTestModel(validFor)
	m.modelInfo = m.model.Artifact.Info
}

func monitorReportFor(date string) selection.Report {
	return selection.Report{
		Version: 1, ID: "report-" + date, AsOf: "2026-10-19", Target: date,
		GeneratedAt: time.Date(2026, 10, 19, 18, 5, 0, 0, market.Shanghai),
		Selected:    []selection.Candidate{{Code: "sh600000", Name: "Test"}},
	}
}

func monitorMinuteBar(t *testing.T, date, clock string, open, high, low, close float64) market.MinuteBar {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04", date+" "+clock, market.Shanghai)
	if err != nil {
		t.Fatalf("parse bar time: %v", err)
	}
	return market.MinuteBar{End: at, Open: open, High: high, Low: low, Close: close, Volume: 1000, Final: true}
}

func monitorAt(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, market.Shanghai)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

func TestNextBarSlotBoundaries(t *testing.T) {
	cases := []struct {
		now        string
		ok         bool
		input      string
		target     string
		skipReason string
	}{
		{"2026-10-20 09:34:59", false, "", "", "before_open"},
		{"2026-10-20 09:35:00", true, "09:35", "09:40", ""},
		{"2026-10-20 11:29:59", true, "11:25", "11:30", ""},
		{"2026-10-20 11:30:00", false, "", "", "lunch_break"},
		{"2026-10-20 13:04:59", false, "", "", "lunch_break"},
		{"2026-10-20 13:05:00", true, "13:05", "13:10", ""},
		{"2026-10-20 14:55:00", true, "14:55", "15:00", ""},
		{"2026-10-20 15:00:00", false, "", "", "after_close"},
	}
	for _, tc := range cases {
		slot := nextBarSlot(monitorAt(t, tc.now))
		if slot.OK != tc.ok {
			t.Fatalf("%s: ok=%v want %v", tc.now, slot.OK, tc.ok)
		}
		if !tc.ok {
			if slot.Reason != tc.skipReason {
				t.Fatalf("%s: reason=%q want %q", tc.now, slot.Reason, tc.skipReason)
			}
			continue
		}
		if slot.InputEnd.Format("15:04") != tc.input || slot.TargetEnd.Format("15:04") != tc.target {
			t.Fatalf("%s: got %s -> %s want %s -> %s", tc.now,
				slot.InputEnd.Format("15:04"), slot.TargetEnd.Format("15:04"), tc.input, tc.target)
		}
	}
}

func TestPredictOncePersistsAndIsIdempotent(t *testing.T) {
	now := monitorAt(t, "2026-10-20 09:35:10")
	m, reader, channel := monitorNewTestMonitor(t, now)
	monitorSetModel(m, "2026-10-20")
	reader.reports["2026-10-19"] = monitorReportFor("2026-10-20")
	reader.minutes["sh600000"] = []market.MinuteBar{monitorMinuteBar(t, "2026-10-20", "09:35", 10, 10.1, 9.9, 10)}

	result, err := m.PredictOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if len(result.Predictions) != 1 || !result.TargetEnd.Equal(monitorAt(t, "2026-10-20 09:40:00")) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !result.Pushed {
		t.Fatalf("expected push enqueue")
	}
	if len(m.ledger.Events) != 1 {
		t.Fatalf("ledger events=%d want 1", len(m.ledger.Events))
	}
	for _, event := range m.ledger.Events {
		if !event.ExpiresAt.Equal(monitorAt(t, "2026-10-20 09:40:00")) {
			t.Fatalf("expiry=%s want target end", event.ExpiresAt)
		}
	}
	if err := m.flush(context.Background(), now); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent=%d want 1", len(channel.sent))
	}

	again, err := m.PredictOnce(context.Background(), monitorAt(t, "2026-10-20 09:36:00"))
	if err != nil {
		t.Fatalf("predict again: %v", err)
	}
	if again.Pushed {
		t.Fatalf("second cycle must not enqueue another push")
	}
	if len(m.ledger.Events) != 1 {
		t.Fatalf("ledger events=%d want 1 after restart-like cycle", len(m.ledger.Events))
	}
	predictions, err := m.DB.ForDate(context.Background(), "2026-10-20")
	if err != nil || len(predictions) != 1 {
		t.Fatalf("persisted predictions=%d err=%v want 1", len(predictions), err)
	}
}

func TestPredictOnceReconcilesArrivedTargetBar(t *testing.T) {
	now := monitorAt(t, "2026-10-20 09:35:10")
	m, reader, _ := monitorNewTestMonitor(t, now)
	monitorSetModel(m, "2026-10-20")
	reader.reports["2026-10-19"] = monitorReportFor("2026-10-20")
	reader.minutes["sh600000"] = []market.MinuteBar{monitorMinuteBar(t, "2026-10-20", "09:35", 10, 10.1, 9.9, 10)}
	if _, err := m.PredictOnce(context.Background(), now); err != nil {
		t.Fatalf("initial predict: %v", err)
	}

	reader.minutes["sh600000"] = append(reader.minutes["sh600000"], monitorMinuteBar(t, "2026-10-20", "09:40", 10, 10.3, 10.1, 10.2))
	next := monitorAt(t, "2026-10-20 09:40:10")
	if _, err := m.PredictOnce(context.Background(), next); err != nil {
		t.Fatalf("second predict: %v", err)
	}
	var status string
	var highPct, lowPct, closePct float64
	err := m.DB.db.QueryRow(`SELECT status,high_pct,low_pct,close_pct FROM prediction_actual
		WHERE code=? AND target_end_unix=?`, "sh600000", monitorAt(t, "2026-10-20 09:40:00").Unix()).
		Scan(&status, &highPct, &lowPct, &closePct)
	if err != nil {
		t.Fatalf("read actual: %v", err)
	}
	if status != "resolved" {
		t.Fatalf("status=%q want resolved", status)
	}
	if monitorAbsDiff(highPct, 0.03) > 1e-9 || monitorAbsDiff(lowPct, 0.01) > 1e-9 || monitorAbsDiff(closePct, 0.02) > 1e-9 {
		t.Fatalf("actual pct high=%v low=%v close=%v", highPct, lowPct, closePct)
	}
}

func monitorAbsDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestPredictOnceRequiresModelValidForToday(t *testing.T) {
	now := monitorAt(t, "2026-10-20 09:35:10")
	m, reader, _ := monitorNewTestMonitor(t, now)
	monitorSetModel(m, "2026-10-19")
	reader.reports["2026-10-19"] = monitorReportFor("2026-10-20")
	reader.minutes["sh600000"] = []market.MinuteBar{monitorMinuteBar(t, "2026-10-20", "09:35", 10, 10.1, 9.9, 10)}

	result, err := m.PredictOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if len(result.Predictions) != 0 || len(result.Skipped) != 1 || result.Skipped[0] != "model_not_valid_for_today" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(m.ledger.Events) != 0 {
		t.Fatalf("stale model must not enqueue")
	}
}

func TestPredictOnceSkipsWithoutReport(t *testing.T) {
	now := monitorAt(t, "2026-10-20 09:35:10")
	m, reader, _ := monitorNewTestMonitor(t, now)
	monitorSetModel(m, "2026-10-20")
	reader.minutes["sh600000"] = []market.MinuteBar{monitorMinuteBar(t, "2026-10-20", "09:35", 10, 10.1, 9.9, 10)}

	result, err := m.PredictOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "report_unavailable" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestReconcileMarksUnresolvedAfterClose(t *testing.T) {
	now := monitorAt(t, "2026-10-20 15:10:00")
	m, _, _ := monitorNewTestMonitor(t, now)
	monitorSetModel(m, "2026-10-20")
	prediction := Prediction{
		Code: "sh600000", Date: "2026-10-20",
		InputEnd: monitorAt(t, "2026-10-20 14:55:00"), TargetEnd: monitorAt(t, "2026-10-20 15:00:00"),
		ModelID: "model-2026-10-20", BasePrice: 10,
		HighPct: 0.01, LowPct: -0.01, ClosePct: 0, Trend: "震荡", Confidence: "低",
		CreatedAt: monitorAt(t, "2026-10-20 14:55:10"),
	}
	if _, err := m.DB.InsertPrediction(context.Background(), prediction); err != nil {
		t.Fatalf("insert prediction: %v", err)
	}
	result, err := m.PredictOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "after_close" {
		t.Fatalf("unexpected result: %+v", result)
	}
	var status string
	err = m.DB.db.QueryRow(`SELECT status FROM prediction_actual WHERE code=? AND target_end_unix=?`,
		"sh600000", prediction.TargetEnd.Unix()).Scan(&status)
	if err != nil {
		t.Fatalf("read actual: %v", err)
	}
	if status != "unresolved" {
		t.Fatalf("status=%q want unresolved", status)
	}
}

func monitorSyntheticSamples(days int) []Sample {
	out := make([]Sample, 0, days)
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, market.Shanghai)
	for i := 0; i < days; i++ {
		input := start.AddDate(0, 0, i)
		out = append(out, Sample{
			Code: "sh600000", Date: input.Format("2006-01-02"), InputEnd: input, TargetEnd: input.Add(5 * time.Minute),
			InputOpen: 10, InputHigh: 10.1, InputLow: 9.9, InputClose: 10, BaselineRange: 0.02,
			Features: make([]float64, len(FeatureNames())),
			Labels:   map[Target]float64{TargetHigh: 0.01, TargetLow: -0.01, TargetClose: 0},
		})
	}
	return out
}

func TestTrainWritesArtifactPointerAndMetadata(t *testing.T) {
	now := monitorAt(t, "2026-10-20 15:30:00")
	m, _, _ := monitorNewTestMonitor(t, now)
	m.Config.Predictor.KeepModels = 1
	if err := os.MkdirAll(filepath.Join(m.Config.DataDir, "models", "intraday", "old-model"), 0700); err != nil {
		t.Fatalf("prepare old model: %v", err)
	}
	m.samplesBuilder = func(Data) ([]Sample, error) { return monitorSyntheticSamples(6), nil }
	m.walker = func(samples []Sample, _ ModelInfo, _ int, _ TrainOptions) (ModelMetrics, TargetMetrics, error) {
		return ModelMetrics{Samples: len(samples), Days: 6}, TargetMetrics{High: 0.01, Low: 0.01, Close: 0.01}, nil
	}
	m.trainer = func(_ []Sample, info ModelInfo, opts TrainOptions) (*Artifact, error) {
		return &Artifact{
			SchemaVersion: ArtifactSchemaVersion, Info: info, Hyperparameters: opts.Hyperparameters,
			Base:      map[Target]float64{TargetHigh: 0.01, TargetLow: -0.01, TargetClose: 0},
			Trees:     map[Target][]Tree{TargetHigh: {}, TargetLow: {}, TargetClose: {}},
			LabelClip: map[Target]float64{TargetHigh: 0.05, TargetLow: 0.05, TargetClose: 0.05},
		}, nil
	}

	info, err := m.Train(context.Background(), now)
	if err != nil {
		t.Fatalf("train: %v", err)
	}
	if info.ValidFor != "2026-10-21" {
		t.Fatalf("valid_for=%q want 2026-10-21", info.ValidFor)
	}
	if m.model == nil || m.model.Artifact.Info.ModelID != info.ModelID {
		t.Fatalf("model was not loaded after training")
	}
	var pointer ModelPointer
	if err := m.Store.Read(modelPointerPath, &pointer); err != nil {
		t.Fatalf("read pointer: %v", err)
	}
	if pointer.ModelID != info.ModelID || pointer.Path != info.ArtifactPath {
		t.Fatalf("pointer=%+v info=%+v", pointer, info)
	}
	var artifact Artifact
	if err := m.Store.Read(info.ArtifactPath, &artifact); err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	loaded, err := LoadModel(raw)
	if err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if loaded.Artifact.Info.ModelID != info.ModelID {
		t.Fatalf("artifact model id mismatch")
	}
	recorded, err := m.DB.ModelInfo(context.Background(), info.ModelID)
	if err != nil {
		t.Fatalf("model info: %v", err)
	}
	if recorded.ArtifactPath != info.ArtifactPath {
		t.Fatalf("recorded path=%q want %q", recorded.ArtifactPath, info.ArtifactPath)
	}
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.ModelID != info.ModelID || status.ModelValidFor != "2026-10-21" {
		t.Fatalf("status=%+v", status)
	}
	if _, err := os.Stat(filepath.Join(m.Config.DataDir, "models", "intraday", "old-model")); !os.IsNotExist(err) {
		t.Fatalf("old model directory was not pruned: %v", err)
	}
}

func TestTrainRejectsTooFewDays(t *testing.T) {
	now := monitorAt(t, "2026-10-20 15:30:00")
	m, _, _ := monitorNewTestMonitor(t, now)
	m.samplesBuilder = func(Data) ([]Sample, error) { return monitorSyntheticSamples(2), nil }
	if _, err := m.Train(context.Background(), now); err == nil {
		t.Fatalf("expected insufficient training days error")
	}
}

func TestTrainingDueSchedulesAfterCloseOnly(t *testing.T) {
	now := monitorAt(t, "2026-10-20 09:00:00")
	m, _, _ := monitorNewTestMonitor(t, now)
	monitorSetModel(m, "2026-10-20")
	if m.trainingDue(now) {
		t.Fatalf("model valid for today must not retrain before close")
	}
	if !m.trainingDue(monitorAt(t, "2026-10-20 15:31:00")) {
		t.Fatalf("expected training due after close for next day")
	}
}

func TestSetPushEnabledExpiresPendingAndStopsNewPushes(t *testing.T) {
	now := monitorAt(t, "2026-10-20 09:35:10")
	m, reader, channel := monitorNewTestMonitor(t, now)
	monitorSetModel(m, "2026-10-20")
	reader.reports["2026-10-19"] = monitorReportFor("2026-10-20")
	reader.minutes["sh600000"] = []market.MinuteBar{monitorMinuteBar(t, "2026-10-20", "09:35", 10, 10.1, 9.9, 10)}
	if _, err := m.PredictOnce(context.Background(), now); err != nil {
		t.Fatalf("initial predict: %v", err)
	}
	if len(m.ledger.Events) != 1 {
		t.Fatalf("ledger events=%d want 1", len(m.ledger.Events))
	}
	if err := m.SetPushEnabled(context.Background(), false); err != nil {
		t.Fatalf("disable push: %v", err)
	}
	if err := m.ledger.Validate(); err != nil {
		t.Fatalf("ledger invalid after disable: %v", err)
	}
	for _, event := range m.ledger.Events {
		for _, state := range event.Channels {
			if state.Status != notify.Expired {
				t.Fatalf("pending delivery status=%q want expired", state.Status)
			}
		}
	}
	if err := m.loadLedger(); err != nil {
		t.Fatalf("reload ledger: %v", err)
	}

	reader.minutes["sh600000"] = append(reader.minutes["sh600000"], monitorMinuteBar(t, "2026-10-20", "09:40", 10, 10.2, 10.1, 10.1))
	next := monitorAt(t, "2026-10-20 09:40:10")
	result, err := m.PredictOnce(context.Background(), next)
	if err != nil {
		t.Fatalf("predict with push disabled: %v", err)
	}
	if result.Pushed || len(m.ledger.Events) != 1 {
		t.Fatalf("push disabled must not enqueue: pushed=%v events=%d", result.Pushed, len(m.ledger.Events))
	}
	predictions, err := m.DB.ForDate(context.Background(), "2026-10-20")
	if err != nil || len(predictions) != 2 {
		t.Fatalf("predictions=%d err=%v want 2", len(predictions), err)
	}
	if err := m.flush(context.Background(), next); err != nil {
		t.Fatalf("flush with push disabled: %v", err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("sent=%d want 0 while push disabled", len(channel.sent))
	}
}

func TestPredictOnceWithRealInference(t *testing.T) {
	now := monitorAt(t, "2026-10-20 14:55:10")
	m, reader, _ := monitorNewTestMonitor(t, now)
	m.inferenceBuilder = BuildInference
	monitorSetModel(m, "2026-10-20")
	reader.reports["2026-10-19"] = monitorReportFor("2026-10-20")
	var minutes []market.MinuteBar
	price := 10.0
	for _, clock := range []string{
		"09:35", "09:40", "09:45", "09:50", "09:55", "10:00", "10:05", "10:10", "10:15", "10:20", "10:25", "10:30",
		"10:35", "10:40", "10:45", "10:50", "10:55", "11:00", "11:05", "11:10", "11:15", "11:20", "11:25", "11:30",
		"13:05", "13:10", "13:15", "13:20", "13:25", "13:30", "13:35", "13:40", "13:45", "13:50", "13:55", "14:00",
		"14:05", "14:10", "14:15", "14:20", "14:25", "14:30", "14:35", "14:40", "14:45", "14:50", "14:55",
	} {
		minutes = append(minutes, monitorMinuteBar(t, "2026-10-20", clock, price, price*1.002, price*0.998, price*1.001))
		price *= 1.001
	}
	reader.minutes["sh600000"] = minutes
	base := time.Date(2026, 6, 1, 15, 0, 0, 0, market.Shanghai)
	for i := 0; i < 60; i++ {
		day := base.AddDate(0, 0, i)
		close := 9 + float64(i)*0.02
		reader.daily["sh600000"] = append(reader.daily["sh600000"], market.Bar{
			Date: day.Format("2006-01-02"), Open: close - 0.05, High: close + 0.1, Low: close - 0.1, Close: close, Volume: 10000,
		})
	}
	result, err := m.PredictOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if len(result.Predictions) != 1 || !result.TargetEnd.Equal(monitorAt(t, "2026-10-20 15:00:00")) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestTrainWithRealBuilders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real GBDT training in short mode")
	}
	now := monitorAt(t, "2026-10-20 15:30:00")
	m, reader, _ := monitorNewTestMonitor(t, now)
	clocks := []string{
		"09:35", "09:40", "09:45", "09:50", "09:55", "10:00", "10:05", "10:10", "10:15", "10:20", "10:25", "10:30",
		"10:35", "10:40", "10:45", "10:50", "10:55", "11:00", "11:05", "11:10", "11:15", "11:20", "11:25", "11:30",
		"13:05", "13:10", "13:15", "13:20", "13:25", "13:30", "13:35", "13:40", "13:45", "13:50", "13:55", "14:00",
		"14:05", "14:10", "14:15", "14:20", "14:25", "14:30", "14:35", "14:40", "14:45", "14:50", "14:55", "15:00",
	}
	start := time.Date(2026, 10, 13, 0, 0, 0, 0, market.Shanghai)
	price := 10.0
	for day := 0; day < 6; day++ {
		date := start.AddDate(0, 0, day).Format("2006-01-02")
		for _, clock := range clocks {
			reader.minutes["sh600000"] = append(reader.minutes["sh600000"],
				monitorMinuteBar(t, date, clock, price, price*1.002, price*0.998, price*1.001))
			price *= 1.0005
		}
	}
	dailyBase := time.Date(2026, 6, 1, 15, 0, 0, 0, market.Shanghai)
	for i := 0; i < 60; i++ {
		day := dailyBase.AddDate(0, 0, i)
		close := 9 + float64(i)*0.02
		reader.daily["sh600000"] = append(reader.daily["sh600000"], market.Bar{
			Date: day.Format("2006-01-02"), Open: close - 0.05, High: close + 0.1, Low: close - 0.1, Close: close, Volume: 10000,
		})
	}
	info, err := m.Train(context.Background(), now)
	if err != nil {
		t.Fatalf("train: %v", err)
	}
	if info.ValidFor != "2026-10-21" || info.Metrics.Samples == 0 || m.model == nil {
		t.Fatalf("unexpected trained model: %+v", info)
	}
	if _, err := m.DB.ModelInfo(context.Background(), info.ModelID); err != nil {
		t.Fatalf("model info: %v", err)
	}
}

func TestTrainingDueHonorsTrainAt(t *testing.T) {
	now := monitorAt(t, "2026-10-20 09:00:00")
	m, _, _ := monitorNewTestMonitor(t, now)
	monitorSetModel(m, "2026-10-20")

	for _, clock := range []string{"2026-10-20 15:00:00", "2026-10-20 15:29:59"} {
		if m.trainingDue(monitorAt(t, clock)) {
			t.Fatalf("%s: trained before train_at", clock)
		}
	}
	if !m.trainingDue(monitorAt(t, "2026-10-20 15:30:00")) {
		t.Fatal("expected next-day training at train_at")
	}

	monitorSetModel(m, "2026-10-21")
	if m.trainingDue(monitorAt(t, "2026-10-20 15:30:00")) {
		t.Fatal("retrained while model already valid for tomorrow")
	}

	m.model = nil
	m.lastTrainAttempt = time.Time{}
	if !m.trainingDue(monitorAt(t, "2026-10-20 10:00:00")) {
		t.Fatal("expected startup training when model is missing before close")
	}
}

func TestTrainExcludesSameSessionSamples(t *testing.T) {
	now := monitorAt(t, "2026-10-20 10:00:00")
	m, _, _ := monitorNewTestMonitor(t, now)
	m.Config.Predictor.MinTrainingDays = 1
	features := make([]float64, len(FeatureNames()))
	m.samplesBuilder = func(Data) ([]Sample, error) {
		return []Sample{
			{Code: "sh600000", Date: "2026-10-19", InputEnd: now.AddDate(0, 0, -1), Features: features},
			{Code: "sh600000", Date: "2026-10-20", InputEnd: now, Features: features},
		}, nil
	}
	m.walker = func([]Sample, ModelInfo, int, TrainOptions) (ModelMetrics, TargetMetrics, error) {
		return ModelMetrics{Samples: 1, Days: 1}, TargetMetrics{}, nil
	}
	var trained []Sample
	m.trainer = func(samples []Sample, info ModelInfo, _ TrainOptions) (*Artifact, error) {
		trained = append([]Sample(nil), samples...)
		artifact := monitorTestModel("unused").Artifact
		artifact.Info = info
		return &artifact, nil
	}
	info, err := m.Train(context.Background(), now)
	if err != nil {
		t.Fatalf("train: %v", err)
	}
	if len(trained) != 1 || trained[0].Date != "2026-10-19" {
		t.Fatalf("trained samples=%+v want only prior session", trained)
	}
	if info.ValidFor != "2026-10-20" || info.DataTo != "2026-10-19" {
		t.Fatalf("unexpected model info: valid_for=%s data_to=%s", info.ValidFor, info.DataTo)
	}
}

func TestTrainRejectsOnlySameSessionSamples(t *testing.T) {
	now := monitorAt(t, "2026-10-20 10:00:00")
	m, _, _ := monitorNewTestMonitor(t, now)
	features := make([]float64, len(FeatureNames()))
	m.samplesBuilder = func(Data) ([]Sample, error) {
		return []Sample{{Code: "sh600000", Date: "2026-10-20", InputEnd: now, Features: features}}, nil
	}
	if _, err := m.Train(context.Background(), now); err == nil {
		t.Fatal("trained on same-session samples")
	}
}
