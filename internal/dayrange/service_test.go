package dayrange

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

type fakeDayRangeStore struct {
	snapshots   map[string][]byte
	models      map[string]ModelRun
	predictions map[string]Prediction
	actuals     map[string]ActualRecord
	evaluations map[string]Evaluation
	audits      []AuditRecord
}

func newFakeDayRangeStore() *fakeDayRangeStore {
	return &fakeDayRangeStore{snapshots: map[string][]byte{}, models: map[string]ModelRun{}, predictions: map[string]Prediction{},
		actuals: map[string]ActualRecord{}, evaluations: map[string]Evaluation{}}
}

func fakeDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (f *fakeDayRangeStore) PutSnapshot(_ context.Context, _, _, _ string, _ Mode, _ time.Time, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	id := fakeDigest(value)
	f.snapshots[id] = raw
	return id, nil
}

func (f *fakeDayRangeStore) LoadSnapshot(_ context.Context, id string, out any) error {
	raw, ok := f.snapshots[id]
	if !ok {
		return sql.ErrNoRows
	}
	return json.Unmarshal(raw, out)
}

func (f *fakeDayRangeStore) SaveModelRun(_ context.Context, run ModelRun) error {
	f.models[run.ModelID] = run
	return nil
}

func (f *fakeDayRangeStore) LoadModelRun(_ context.Context, id string) (ModelRun, error) {
	run, ok := f.models[id]
	if !ok {
		return ModelRun{}, sql.ErrNoRows
	}
	return run, nil
}

func (f *fakeDayRangeStore) LatestValidatedModelBefore(_ context.Context, targetDate string, maxAgeDays int) (ModelRun, error) {
	cutoff := mustDay(targetDate).AddDate(0, 0, -maxAgeDays).Format(time.DateOnly)
	var selected ModelRun
	for _, run := range f.models {
		if run.Status != ModelStatusValidated || run.Kind != modelKind || run.ValidFor > targetDate || run.TrainTo < cutoff {
			continue
		}
		if selected.ModelID == "" || run.TrainTo > selected.TrainTo {
			selected = run
		}
	}
	if selected.ModelID == "" {
		return ModelRun{}, sql.ErrNoRows
	}
	return selected, nil
}

func (f *fakeDayRangeStore) LatestModelForValidFor(_ context.Context, validFor string) (ModelRun, error) {
	var selected ModelRun
	for _, run := range f.models {
		if run.ValidFor != validFor || (run.Kind != "baseline" && run.Kind != modelKind) {
			continue
		}
		if selected.CreatedAt.IsZero() || run.CreatedAt.After(selected.CreatedAt) {
			selected = run
		}
	}
	if selected.ModelID == "" {
		return ModelRun{}, sql.ErrNoRows
	}
	return selected, nil
}

func (f *fakeDayRangeStore) SavePrediction(_ context.Context, prediction Prediction) (bool, error) {
	if _, ok := f.predictions[prediction.PredictionID]; ok {
		return false, nil
	}
	f.predictions[prediction.PredictionID] = prediction
	return true, nil
}

func (f *fakeDayRangeStore) LoadPredictions(_ context.Context, targetDate, code string) ([]PredictionRecord, error) {
	keys := make([]string, 0, len(f.predictions))
	for key, prediction := range f.predictions {
		if prediction.TargetDate != targetDate || code != "" && prediction.Code != code {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]PredictionRecord, 0, len(keys))
	for _, key := range keys {
		prediction := f.predictions[key]
		var actual *ActualRecord
		if record, ok := f.actuals[prediction.Code+"|"+prediction.TargetDate]; ok {
			copied := record
			actual = &copied
		}
		out = append(out, PredictionRecord{Prediction: prediction, Actual: actual})
	}
	return out, nil
}

func (f *fakeDayRangeStore) SaveActual(_ context.Context, actual ActualRecord) error {
	f.actuals[actual.Code+"|"+actual.TargetDate] = actual
	return nil
}

func (f *fakeDayRangeStore) LoadActual(_ context.Context, code, targetDate string) (*ActualRecord, error) {
	actual, ok := f.actuals[code+"|"+targetDate]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &actual, nil
}

func (f *fakeDayRangeStore) SaveEvaluation(_ context.Context, evaluation Evaluation) (string, error) {
	evaluation.ID = fakeDigest(evaluation)
	f.evaluations[evaluation.ID] = evaluation
	return evaluation.ID, nil
}

func (f *fakeDayRangeStore) LatestEvaluation(_ context.Context, asof string) (Evaluation, error) {
	var selected Evaluation
	for _, evaluation := range f.evaluations {
		if asof != "" && evaluation.AsOf != asof {
			continue
		}
		if selected.ID == "" || evaluation.CreatedAt.After(selected.CreatedAt) {
			selected = evaluation
		}
	}
	if selected.ID == "" {
		return Evaluation{}, sql.ErrNoRows
	}
	return selected, nil
}

func (f *fakeDayRangeStore) SaveAudit(_ context.Context, snapshotID, asof string, createdAt time.Time, audit Audit) (string, error) {
	record := AuditRecord{ID: fakeDigest(struct {
		Snapshot string
		Audit    Audit
	}{snapshotID, audit}), SnapshotID: snapshotID, AsOf: asof, CreatedAt: createdAt, Audit: audit}
	f.audits = append(f.audits, record)
	return record.ID, nil
}

func (f *fakeDayRangeStore) LatestAudit(_ context.Context, asof string) (AuditRecord, error) {
	for i := len(f.audits) - 1; i >= 0; i-- {
		if asof == "" || f.audits[i].AsOf == asof {
			return f.audits[i], nil
		}
	}
	return AuditRecord{}, sql.ErrNoRows
}

type fakeDayRangeMarket struct{ data MarketData }

func (f fakeDayRangeMarket) LoadDayRangeData(context.Context, []config.Stock, string, string) (MarketData, error) {
	return f.data, nil
}

type recordingDayRangeMarket struct {
	data          MarketData
	from, through string
}

func (f *recordingDayRangeMarket) LoadDayRangeData(_ context.Context, _ []config.Stock, from, through string) (MarketData, error) {
	f.from, f.through = from, through
	return f.data, nil
}

func mustDay(value string) time.Time {
	day, err := time.ParseInLocation(time.DateOnly, value, shanghai)
	if err != nil {
		panic(err)
	}
	return day
}

func serviceConfig() config.Config {
	c := config.Defaults()
	c.DayRange.Enabled = true
	c.DayRange.HistoryDays = 365
	c.Calendar.ValidFrom = "2025-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试银行", Sector: "银行"}}
	return c
}

func serviceDays(c config.Config, start, end string) []string {
	days, err := calendar.New(c).TradingDays(start, end)
	if err != nil {
		panic(err)
	}
	return days
}

func TestLoadDataUsesExchangeCalendarBeforeConfiguredCalendar(t *testing.T) {
	c := serviceConfig()
	c.Calendar.ValidFrom = "2026-01-01"
	market := &recordingDayRangeMarket{}
	service := NewService(c, newFakeDayRangeStore(), market, nil)
	if _, err := service.loadData(context.Background(), "2026-10-21"); err != nil {
		t.Fatal(err)
	}
	if market.from != "2025-04-24" || market.through != "2026-10-21" {
		t.Fatalf("unexpected dayrange data window: %s through %s", market.from, market.through)
	}
}

func newDayRangeServiceForTest(t *testing.T, now time.Time, data MarketData) (*Service, *fakeDayRangeStore) {
	t.Helper()
	c := serviceConfig()
	store := newFakeDayRangeStore()
	service := NewService(c, store, fakeDayRangeMarket{data: data}, nil)
	service.Now = func() time.Time { return now }
	service.BuildVersion = "test"
	return service, store
}

func TestPredictUsesBaselineAndIsIdempotent(t *testing.T) {
	c := serviceConfig()
	days := serviceDays(c, "2025-09-01", "2026-10-19")
	data := premarketData(days, "sz000001")
	now := time.Date(2026, 10, 20, 8, 45, 0, 0, shanghai)
	service, store := newDayRangeServiceForTest(t, now, data)
	result, err := service.Predict(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetDate != "2026-10-20" || len(result.Predictions) != 1 || result.Predictions[0].Source != "baseline" {
		t.Fatalf("unexpected baseline prediction: %+v", result)
	}
	prediction := result.Predictions[0]
	if prediction.ReferencePrice <= 0 || prediction.PointHigh < prediction.PointLow || prediction.InputSnapshotID == "" || prediction.InputThrough != "2026-10-19" {
		t.Fatalf("invalid frozen prediction: %+v", prediction)
	}
	if !prediction.AsOf.Equal(now) {
		t.Fatalf("prediction as-of was not the actual generation time: %+v", prediction.AsOf)
	}
	again, err := service.Predict(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Predictions) != 0 || len(again.Skipped) != 1 || !containsString(again.Skipped, "sz000001:already_published") {
		t.Fatalf("prediction rerun was not idempotent: %+v", again)
	}
	if len(store.predictions) != 1 {
		t.Fatalf("duplicate predictions saved: %d", len(store.predictions))
	}
	replayed, err := service.Replay(context.Background(), "2026-10-20", "sz000001")
	if err != nil || len(replayed) != 1 || !replayed[0].Match {
		t.Fatalf("baseline replay failed: %+v %v", replayed, err)
	}

	afterOpen := time.Date(2026, 10, 20, 10, 0, 0, 0, shanghai)
	service.Now = func() time.Time { return afterOpen }
	if _, err := service.Predict(context.Background(), "2026-10-20", ""); err == nil {
		t.Fatal("post-open forecast was backfilled")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestPredictCanUseOnlyAValidatedModel(t *testing.T) {
	c := serviceConfig()
	days := serviceDays(c, "2025-09-01", "2026-10-20")
	data := premarketData(days, "sz000001")
	now := time.Date(2026, 10, 20, 8, 45, 0, 0, shanghai)
	service, store := newDayRangeServiceForTest(t, now, data)
	samples := trainingSamples(90)
	info := ModelInfo{SchemaVersion: ModelSchemaVersion, CodeVersion: "test", CreatedAt: now.Add(-time.Hour), TrainFrom: "2025-01-01", TrainTo: "2026-10-19",
		CalibrationFrom: "2025-01-02", CalibrationTo: "2025-02-01", ValidationFrom: "2025-02-02", ValidationTo: "2025-03-01", ValidFor: "2026-10-20"}
	artifact, err := TrainPointModel(context.Background(), samples[:60], info, TrainOptions{Hyperparameters: Hyperparameters{Trees: 4, MaxDepth: 2, MinLeaf: 5, LearningRate: .05, L2: 1, Bins: 16, Seed: 1949}, LabelClip: .5})
	if err != nil {
		t.Fatal(err)
	}
	if err := Calibrate(artifact, samples[60:]); err != nil {
		t.Fatal(err)
	}
	if err := SealArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveModelRun(context.Background(), ModelRun{ModelID: artifact.ModelID, Kind: modelKind, Status: ModelStatusValidated, FeatureVersion: FeatureVersion,
		CodeVersion: "test", CreatedAt: now.Add(-time.Hour), TrainFrom: info.TrainFrom, TrainTo: info.TrainTo, CalibrationFrom: info.CalibrationFrom,
		CalibrationTo: info.CalibrationTo, ValidationFrom: info.ValidationFrom, ValidationTo: info.ValidationTo, ValidFor: info.ValidFor, Checksum: artifact.Checksum, Artifact: raw}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Predict(context.Background(), "2026-10-20", "")
	if err != nil || len(result.Predictions) != 1 || result.Predictions[0].Source != "model" || result.Predictions[0].ModelID != artifact.ModelID {
		t.Fatalf("validated model was not used: %+v %v", result, err)
	}
}

func TestPredictInvalidatesCorruptModelAndFallsBackToBaseline(t *testing.T) {
	c := serviceConfig()
	days := serviceDays(c, "2025-09-01", "2026-10-20")
	data := premarketData(days, "sz000001")
	now := time.Date(2026, 10, 20, 8, 45, 0, 0, shanghai)
	service, store := newDayRangeServiceForTest(t, now, data)
	modelID := "corrupt-model"
	if err := store.SaveModelRun(context.Background(), ModelRun{ModelID: modelID, Kind: modelKind, Status: ModelStatusValidated,
		FeatureVersion: FeatureVersion, CodeVersion: "test", CreatedAt: now.Add(-time.Hour), TrainFrom: "2025-01-01", TrainTo: "2026-10-19",
		ValidFor: "2026-10-20", Artifact: []byte("not-json")}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Predict(context.Background(), "2026-10-20", "")
	if err != nil || len(result.Predictions) != 1 || result.Predictions[0].Source != "baseline" {
		t.Fatalf("corrupt model did not fall back to baseline: %+v %v", result, err)
	}
	run := store.models[modelID]
	if run.Status != ModelStatusInvalid || !strings.HasPrefix(run.Reason, "artifact_invalid:") {
		t.Fatalf("corrupt model was not marked invalid: %+v", run)
	}
}

func TestReconcileRequiresTradedStatusAndCompletedBar(t *testing.T) {
	c := serviceConfig()
	days := serviceDays(c, "2025-09-01", "2026-10-20")
	data := premarketData(days, "sz000001")
	now := time.Date(2026, 10, 20, 18, 35, 0, 0, shanghai)
	service, store := newDayRangeServiceForTest(t, now, data)
	result, err := service.Reconcile(context.Background(), "")
	if err != nil || result.Resolved != 1 || result.Unresolved != 0 || store.actuals["sz000001|2026-10-20"].Status != "resolved" {
		t.Fatalf("valid actual was not reconciled: %+v %v", result, err)
	}

	suspended := premarketData(days, "sz000001")
	suspended.Series[0].Raw = suspended.Series[0].Raw[:len(suspended.Series[0].Raw)-1]
	suspended.Series[0].Adjusted = suspended.Series[0].Adjusted[:len(suspended.Series[0].Adjusted)-1]
	suspended.Statuses["sz000001"]["2026-10-20"] = market.SessionStatus{Code: "sz000001", Date: "2026-10-20", State: market.SessionSuspended}
	service, store = newDayRangeServiceForTest(t, now, suspended)
	result, err = service.Reconcile(context.Background(), "2026-10-20")
	if err != nil || result.Unresolved != 1 || store.actuals["sz000001|2026-10-20"].Reason != "target_suspended" {
		t.Fatalf("suspended actual was not left unresolved: %+v %v", result, err)
	}

	unknown := premarketData(days, "sz000001")
	delete(unknown.Statuses["sz000001"], "2026-10-20")
	service, store = newDayRangeServiceForTest(t, now, unknown)
	result, err = service.Reconcile(context.Background(), "2026-10-20")
	if err != nil || result.Unresolved != 1 || store.actuals["sz000001|2026-10-20"].Reason != "target_status_unknown" {
		t.Fatalf("unknown session was accepted: %+v %v", result, err)
	}
}

func TestTrainWithInsufficientHistoryKeepsBaseline(t *testing.T) {
	c := serviceConfig()
	days := serviceDays(c, "2026-08-01", "2026-10-20")
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, shanghai)
	service, store := newDayRangeServiceForTest(t, now, premarketData(days, "sz000001"))
	result, err := service.Train(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != string(ModelStatusInsufficient) || result.Baseline.ModelID != BaselineModelID {
		t.Fatalf("insufficient history did not preserve baseline: %+v", result)
	}
	if _, ok := store.models[BaselineModelID]; !ok {
		t.Fatal("baseline training attempt was not recorded")
	}
	if _, err := service.Store.LatestModelForValidFor(context.Background(), result.ValidFor); err != nil {
		t.Fatalf("daily training attempt would be retried: %v", err)
	}
}

func TestStatusReportsMissingStoreAsUnavailable(t *testing.T) {
	service := NewService(config.Defaults(), nil, nil, nil)
	status, err := service.Status(context.Background(), "")
	if err != nil || status.Available || !containsString(status.Issues, "dayrange_store_unavailable") {
		t.Fatalf("missing store status is misleading: %+v %v", status, err)
	}
	service = NewService(config.Defaults(), newFakeDayRangeStore(), nil, nil)
	if _, err := service.Predictions(context.Background(), "", ""); err == nil {
		t.Fatal("empty target accepted")
	}
	if _, err := service.Evaluation(context.Background(), "2026-10-20"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing evaluation should be not found: %v", err)
	}
}
