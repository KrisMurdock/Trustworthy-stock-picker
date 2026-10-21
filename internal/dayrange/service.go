package dayrange

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

const maxModelAgeDays = 14
const runPollInterval = 15 * time.Second
const gateMinimumDays = 20
const gateMinimumSamples = 100

type Persistence interface {
	PutSnapshot(context.Context, string, string, string, Mode, time.Time, any) (string, error)
	LoadSnapshot(context.Context, string, any) error
	SaveModelRun(context.Context, ModelRun) error
	LoadModelRun(context.Context, string) (ModelRun, error)
	LatestValidatedModelBefore(context.Context, string, int) (ModelRun, error)
	LatestModelForValidFor(context.Context, string) (ModelRun, error)
	SavePrediction(context.Context, Prediction) (bool, error)
	LoadPredictions(context.Context, string, string) ([]PredictionRecord, error)
	SaveActual(context.Context, ActualRecord) error
	LoadActual(context.Context, string, string) (*ActualRecord, error)
	SaveEvaluation(context.Context, Evaluation) (string, error)
	LatestEvaluation(context.Context, string) (Evaluation, error)
	SaveAudit(context.Context, string, string, time.Time, Audit) (string, error)
	LatestAudit(context.Context, string) (AuditRecord, error)
}

type modelPruner interface {
	PruneModelRuns(context.Context, int) error
}

type MarketSource interface {
	LoadDayRangeData(context.Context, []config.Stock, string, string) (MarketData, error)
}

type Service struct {
	Config       config.Config
	Store        Persistence
	Market       MarketSource
	Log          *logging.Logger
	Now          func() time.Time
	BuildVersion string
	TrainOptions TrainOptions

	deliveryMu     sync.Mutex
	deliveryStore  DeliveryStore
	noticeChannels []notify.Channel
	ledger         notify.Ledger
}

func NewService(c config.Config, store Persistence, market MarketSource, log *logging.Logger) *Service {
	if log == nil {
		log = logging.Nop()
	}
	return &Service{Config: c, Store: store, Market: market, Log: log, Now: time.Now, BuildVersion: "development", TrainOptions: DefaultTrainOptions()}
}

type TrainResult struct {
	AsOf       string      `json:"as_of"`
	ValidFor   string      `json:"valid_for"`
	Status     string      `json:"status"`
	DatasetID  string      `json:"dataset_id"`
	AuditID    string      `json:"audit_id,omitempty"`
	Audit      Audit       `json:"audit"`
	Baseline   ModelView   `json:"baseline"`
	Candidate  *ModelView  `json:"candidate,omitempty"`
	Evaluation *Evaluation `json:"evaluation,omitempty"`
}

func (s *Service) Train(ctx context.Context, asof string, auditOnly bool) (TrainResult, error) {
	result := TrainResult{}
	if s == nil || s.Store == nil || s.Market == nil {
		return result, errors.New("dayrange service is incomplete")
	}
	if !s.Config.DayRange.Enabled {
		return result, errors.New("dayrange is disabled")
	}
	now := s.now()
	completed, validFor, err := s.completedAndNext(asof, now)
	if err != nil {
		return result, err
	}
	result.AsOf, result.ValidFor = completed, validFor
	data, err := s.loadData(ctx, completed)
	if err != nil {
		return result, err
	}
	dataset, err := BuildDataset(ctx, s.Config.DayRange, s.Config.Stocks, data, completed, now)
	if err != nil {
		return result, err
	}
	datasetID, err := s.Store.PutSnapshot(ctx, "training", "", completed, ModePremarket, now, dataset)
	if err != nil {
		return result, err
	}
	result.DatasetID = datasetID
	result.Audit = dataset.Audit
	auditID, err := s.Store.SaveAudit(ctx, datasetID, completed, now, dataset.Audit)
	if err != nil {
		return result, err
	}
	result.AuditID = auditID
	baselineRun := ModelRun{ModelID: BaselineModelID, Kind: "baseline", Status: ModelStatusValidated, FeatureVersion: FeatureVersion,
		CodeVersion: s.BuildVersion, CreatedAt: now, TrainFrom: dataset.From, TrainTo: completed, ValidFor: validFor}
	result.Baseline = baselineView(baselineRun)
	if auditOnly {
		result.Status = "audit_only"
		if err := s.Store.SaveModelRun(ctx, baselineRun); err != nil {
			return result, err
		}
		return result, nil
	}
	split, err := splitChronologically(dataset.Samples, completed, s.Config.DayRange)
	if err != nil {
		baselineRun.Reason = err.Error()
		if saveErr := s.Store.SaveModelRun(ctx, baselineRun); saveErr != nil {
			return result, saveErr
		}
		result.Status = string(ModelStatusInsufficient)
		return result, nil
	}
	info := ModelInfo{SchemaVersion: ModelSchemaVersion, CodeVersion: s.BuildVersion, CreatedAt: now,
		TrainFrom: split.trainFrom, TrainTo: split.trainTo, CalibrationFrom: split.calibrationFrom, CalibrationTo: split.calibrationTo,
		ValidationFrom: split.validationFrom, ValidationTo: split.validationTo, ValidFor: validFor}
	if err := ValidateInfo(info); err != nil {
		return result, err
	}
	artifact, err := TrainPointModel(ctx, split.train, info, s.TrainOptions)
	if err != nil {
		return result, err
	}
	if err := Calibrate(artifact, split.calibration); err != nil {
		baselineRun.Reason = err.Error()
		_ = s.Store.SaveModelRun(ctx, baselineRun)
		result.Status = string(ModelStatusInsufficient)
		return result, nil
	}
	if err := SealArtifact(artifact); err != nil {
		return result, err
	}
	model := &Model{Artifact: *artifact}
	candidateReport, baselineReport, err := EvaluateValidation(ctx, split.validation, artifact.ModelID, func(sample Sample) (Forecast, error) {
		return model.Predict(sample.Snapshot.Values(), sample.Snapshot.ReferencePrice)
	})
	if err != nil {
		return result, err
	}
	gate := CheckGate(candidateReport, baselineReport, gateMinimumDays, gateMinimumSamples)
	status := ModelStatusResearch
	if gate.Passed {
		status = ModelStatusValidated
	}
	reason := strings.Join(gate.Reasons, ",")
	artifactBytes, err := MarshalArtifact(artifact)
	if err != nil {
		return result, err
	}
	candidateRun := ModelRun{ModelID: artifact.ModelID, Kind: artifact.Kind, Status: status, FeatureVersion: FeatureVersion,
		CodeVersion: s.BuildVersion, CreatedAt: now, TrainFrom: split.trainFrom, TrainTo: split.trainTo,
		CalibrationFrom: split.calibrationFrom, CalibrationTo: split.calibrationTo, ValidationFrom: split.validationFrom,
		ValidationTo: split.validationTo, ValidFor: validFor, Metrics: candidateReport.Metrics, Checksum: artifact.Checksum,
		Artifact: artifactBytes, Reason: reason}
	baselineRun.TrainFrom, baselineRun.TrainTo = split.trainFrom, split.trainTo
	baselineRun.Metrics = baselineReport.Metrics
	evaluation := Evaluation{ModelID: artifact.ModelID, BaselineID: BaselineModelID, Mode: ModePremarket, AsOf: completed,
		ValidationFrom: split.validationFrom, ValidationTo: split.validationTo, MetricsVersion: EvaluationSchemaVersion,
		Candidate: candidateReport, Baseline: baselineReport, Gate: gate, CreatedAt: now}
	evaluation.ID, err = s.Store.SaveEvaluation(ctx, evaluation)
	if err != nil {
		return result, err
	}
	if err := s.Store.SaveModelRun(ctx, candidateRun); err != nil {
		return result, err
	}
	if err := s.Store.SaveModelRun(ctx, baselineRun); err != nil {
		return result, err
	}
	if pruner, ok := s.Store.(modelPruner); ok {
		if err := pruner.PruneModelRuns(ctx, s.Config.DayRange.KeepModels); err != nil {
			s.Log.Warn("Dayrange model retention failed", "keep_models", s.Config.DayRange.KeepModels, "error", err.Error())
		}
	}
	result.Status = string(status)
	candidateView := artifact.View()
	candidateView.Status = string(status)
	candidateView.Reason = reason
	candidateView.MetricsSummary = summaryMetrics(candidateReport)
	result.Candidate = &candidateView
	result.Evaluation = &evaluation
	result.Baseline = baselineView(baselineRun)
	s.Log.Info("Dayrange model trained", "as_of", completed, "valid_for", validFor, "status", status, "model_id", artifact.ModelID)
	return result, nil
}

type chronologicalSplit struct {
	train, calibration, validation []Sample
	trainFrom, trainTo             string
	calibrationFrom, calibrationTo string
	validationFrom, validationTo   string
}

func splitChronologically(samples []Sample, asof string, cfg config.DayRange) (chronologicalSplit, error) {
	dates := []string{}
	seen := map[string]bool{}
	for _, sample := range samples {
		if sample.Actual == nil || sample.TrainingReason != "" {
			continue
		}
		if !seen[sample.Snapshot.TargetDate] {
			seen[sample.Snapshot.TargetDate] = true
			dates = append(dates, sample.Snapshot.TargetDate)
		}
	}
	sort.Strings(dates)
	if len(dates) < cfg.MinTrainingDays+cfg.CalibrationDays+cfg.ValidationDays {
		return chronologicalSplit{}, fmt.Errorf("dayrange needs at least %d chronological resolved trading days", cfg.MinTrainingDays+cfg.CalibrationDays+cfg.ValidationDays)
	}
	validationDates := dates[len(dates)-cfg.ValidationDays:]
	calibrationDates := dates[len(dates)-cfg.ValidationDays-cfg.CalibrationDays : len(dates)-cfg.ValidationDays]
	trainDates := dates[:len(dates)-cfg.ValidationDays-cfg.CalibrationDays]
	if len(trainDates) < cfg.MinTrainingDays {
		return chronologicalSplit{}, errors.New("dayrange training window is shorter than min_training_days")
	}
	out := chronologicalSplit{trainFrom: trainDates[0], trainTo: trainDates[len(trainDates)-1],
		calibrationFrom: calibrationDates[0], calibrationTo: calibrationDates[len(calibrationDates)-1],
		validationFrom: validationDates[0], validationTo: validationDates[len(validationDates)-1]}
	for _, sample := range samples {
		if sample.Actual == nil || sample.TrainingReason != "" {
			continue
		}
		switch {
		case sample.Snapshot.TargetDate <= out.trainTo:
			out.train = append(out.train, sample)
		case sample.Snapshot.TargetDate <= out.calibrationTo:
			out.calibration = append(out.calibration, sample)
		case sample.Snapshot.TargetDate <= out.validationTo:
			out.validation = append(out.validation, sample)
		}
	}
	if len(out.train) == 0 || len(out.calibration) < 10 || len(out.validation) == 0 {
		return chronologicalSplit{}, errors.New("dayrange chronological split has no usable samples")
	}
	return out, nil
}

type PredictionResult struct {
	TargetDate  string       `json:"target_date"`
	Mode        Mode         `json:"mode"`
	GeneratedAt time.Time    `json:"generated_at"`
	Predictions []Prediction `json:"predictions"`
	Skipped     []string     `json:"skipped,omitempty"`
	Source      string       `json:"source"`
}

func (s *Service) Predict(ctx context.Context, target string, code string) (PredictionResult, error) {
	result := PredictionResult{Mode: ModePremarket}
	if s == nil || s.Store == nil || s.Market == nil {
		return result, errors.New("dayrange service is incomplete")
	}
	if !s.Config.DayRange.Enabled {
		return result, errors.New("dayrange is disabled")
	}
	now := s.now()
	cal := calendar.New(s.Config)
	completed, err := cal.Completed(now)
	if err != nil {
		return result, err
	}
	expectedTarget, err := cal.Next(completed)
	if err != nil {
		return result, err
	}
	if target == "" {
		target = expectedTarget
	} else if target != expectedTarget {
		return result, errors.New("dayrange target must be the next trading day after the completed session")
	}
	if !config.ValidCode(code) && code != "" {
		return result, errors.New("invalid dayrange stock code")
	}
	if ok, err := cal.Trading(target); err != nil || !ok {
		return result, errors.New("dayrange target must be a covered trading day")
	}
	open, _ := time.ParseInLocation("2006-01-02 15:04", target+" 09:30", shanghai)
	if !now.Before(open) {
		return result, errors.New("dayrange target open has passed; premarket forecasts cannot be backfilled")
	}
	data, err := s.loadData(ctx, completed)
	if err != nil {
		return result, err
	}
	data.TradingDays = append(data.TradingDays, target)
	snapshots, err := BuildPredictionSnapshots(ctx, s.Config.DayRange, s.Config.Stocks, data, target, now)
	if err != nil {
		return result, err
	}
	var model *Model
	var modelRun ModelRun
	modelRun, err = s.Store.LatestValidatedModelBefore(ctx, target, maxModelAgeDays)
	if err == nil {
		model, err = LoadArtifact(modelRun.Artifact)
		if err != nil {
			if invalidateErr := s.invalidateModel(ctx, modelRun, err); invalidateErr != nil {
				return result, invalidateErr
			}
			model = nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	result.TargetDate = target
	result.GeneratedAt = now
	existing := map[string]bool{}
	records, err := s.Store.LoadPredictions(ctx, target, "")
	if err != nil {
		return result, err
	}
	for _, record := range records {
		existing[record.Code] = true
	}
	for _, item := range snapshots {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if code != "" && item.Snapshot.Code != code {
			continue
		}
		if existing[item.Snapshot.Code] {
			result.Skipped = append(result.Skipped, item.Snapshot.Code+":already_published")
			continue
		}
		if item.Reason != "" {
			result.Skipped = append(result.Skipped, item.Snapshot.Code+":"+item.Reason)
			continue
		}
		forecast := *item.Snapshot.Baseline
		modelID, source := BaselineModelID, "baseline"
		if model != nil {
			candidate, predictErr := model.Predict(item.Snapshot.Values(), item.Snapshot.ReferencePrice)
			if predictErr == nil {
				forecast, modelID, source = candidate, model.Artifact.ModelID, "model"
			} else {
				s.Log.Warn("Dayrange inference failed; using baseline", "code", item.Snapshot.Code, "error", predictErr.Error())
			}
		}
		prediction := Prediction{Code: item.Snapshot.Code, TargetDate: target, Mode: ModePremarket, AsOf: item.Snapshot.AsOf,
			InputThrough: item.Snapshot.InputThrough, ModelID: modelID, Source: source, FeatureVersion: FeatureVersion,
			ReferencePrice: item.Snapshot.ReferencePrice, PointHigh: forecast.High.Point, PointLow: forecast.Low.Point,
			QuantilesHigh: forecast.High.Quantiles, QuantilesLow: forecast.Low.Quantiles, IntervalHigh: forecast.High.Interval,
			IntervalLow: forecast.Low.Interval, BaselineModelID: BaselineModelID, Status: "published", CreatedAt: now}
		prediction.PredictionID = PredictionID(prediction)
		snapshotID, err := s.Store.PutSnapshot(ctx, "prediction", prediction.Code, target, ModePremarket, now, item.Snapshot)
		if err != nil {
			return result, err
		}
		prediction.InputSnapshotID = snapshotID
		if _, err := s.Store.SavePrediction(ctx, prediction); err != nil {
			return result, err
		}
		result.Predictions = append(result.Predictions, prediction)
	}
	if len(result.Predictions) > 0 {
		result.Source = result.Predictions[0].Source
	}
	records, err = s.Store.LoadPredictions(ctx, target, "")
	if err != nil {
		return result, err
	}
	if err := s.publishSavedPremarket(ctx, target, records, result.Skipped, now); err != nil {
		return result, err
	}
	s.Log.Info("Dayrange premarket predictions generated", "target_date", target, "published", len(result.Predictions), "skipped", len(result.Skipped), "source", result.Source)
	return result, nil
}

type ReconcileResult struct {
	TargetDate string         `json:"target_date"`
	Resolved   int            `json:"resolved"`
	Unresolved int            `json:"unresolved"`
	Actuals    []ActualRecord `json:"actuals"`
}

func (s *Service) Reconcile(ctx context.Context, target string) (ReconcileResult, error) {
	result := ReconcileResult{}
	if s == nil || s.Store == nil || s.Market == nil {
		return result, errors.New("dayrange service is incomplete")
	}
	if !s.Config.DayRange.Enabled {
		return result, errors.New("dayrange is disabled")
	}
	now := s.now()
	if target == "" {
		completed, err := calendar.New(s.Config).Completed(now)
		if err != nil {
			return result, err
		}
		target = completed
	}
	cal := calendar.New(s.Config)
	latest, err := cal.Completed(now)
	if err != nil {
		return result, err
	}
	if target > latest {
		return result, errors.New("dayrange reconcile target is not a completed trading day")
	}
	if ok, err := cal.Trading(target); err != nil || !ok {
		return result, errors.New("dayrange reconcile target must be a covered trading day")
	}
	data, err := s.loadData(ctx, target)
	if err != nil {
		return result, err
	}
	series := map[string]InputSeries{}
	for _, item := range data.Series {
		series[item.Stock.Code] = item
	}
	for _, stock := range s.Config.Stocks {
		item := series[stock.Code]
		actual := ActualRecord{Code: stock.Code, TargetDate: target, ObservedThrough: target, ResolvedAt: now, Status: "unresolved"}
		bar, ok := barsByDate(item.Raw)[target]
		status := data.Statuses[stock.Code][target]
		switch {
		case status.State == market.SessionSuspended && ok:
			actual.Reason = "source_inconsistent"
		case status.State == market.SessionSuspended:
			actual.Reason = "target_suspended"
		case status.State != market.SessionTraded:
			actual.Reason = "target_status_unknown"
		case ok && !validBar(bar):
			actual.Reason = "invalid_prices"
		case ok && item.RawSource[target] == "":
			actual.Reason = "source_inconsistent"
		case ok:
			actual.Status = "resolved"
			actual.Actual = &Extremes{High: bar.High, Low: bar.Low}
			actual.Source = item.RawSource[target]
			result.Resolved++
		default:
			actual.Reason = "target_status_unknown"
		}
		if actual.Status != "resolved" {
			result.Unresolved++
		}
		if err := s.Store.SaveActual(ctx, actual); err != nil {
			return result, err
		}
		result.Actuals = append(result.Actuals, actual)
	}
	result.TargetDate = target
	s.Log.Info("Dayrange actuals reconciled", "target_date", target, "resolved", result.Resolved, "unresolved", result.Unresolved)
	return result, nil
}

type Status struct {
	SchemaVersion     int          `json:"schema_version"`
	Enabled           bool         `json:"enabled"`
	Available         bool         `json:"available"`
	Date              string       `json:"date"`
	ActiveModel       *ModelView   `json:"active_model,omitempty"`
	BaselineModelID   string       `json:"baseline_model_id"`
	PredictionCount   int          `json:"prediction_count"`
	ResolvedActuals   int          `json:"resolved_actuals"`
	UnresolvedActuals int          `json:"unresolved_actuals"`
	Audit             *AuditRecord `json:"audit,omitempty"`
	Evaluation        *Evaluation  `json:"evaluation,omitempty"`
	Issues            []string     `json:"issues,omitempty"`
}

func (s *Service) Status(ctx context.Context, asof string) (Status, error) {
	status := Status{SchemaVersion: 1, Enabled: s != nil && s.Config.DayRange.Enabled, Available: s != nil && s.Store != nil, BaselineModelID: BaselineModelID}
	if s == nil || s.Store == nil {
		status.Issues = append(status.Issues, "dayrange_store_unavailable")
		return status, nil
	}
	statusDate, evidenceDate := asof, asof
	if statusDate == "" {
		cal := calendar.New(s.Config)
		completed, err := cal.Completed(s.now())
		if err != nil {
			status.Issues = append(status.Issues, "calendar_unavailable")
		} else {
			evidenceDate = completed
			target, err := cal.Next(completed)
			if err != nil {
				status.Issues = append(status.Issues, "calendar_unavailable")
			} else {
				statusDate = target
			}
		}
	}
	if statusDate == "" {
		if audit, err := s.Store.LatestAudit(ctx, ""); err == nil {
			statusDate = audit.AsOf
			if evidenceDate == "" {
				evidenceDate = statusDate
			}
		}
	}
	if evidenceDate == "" {
		evidenceDate = statusDate
	}
	status.Date = statusDate
	if audit, err := s.Store.LatestAudit(ctx, evidenceDate); err == nil {
		status.Audit = &audit
		if audit.Audit.Unresolved > 0 {
			status.Issues = append(status.Issues, "audit_has_unresolved_samples")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		status.Issues = append(status.Issues, "audit_read_failed")
	}
	predictions, err := s.Store.LoadPredictions(ctx, statusDate, "")
	if err == nil {
		status.PredictionCount = len(predictions)
		for _, record := range predictions {
			if record.Actual != nil && record.Actual.Status == "resolved" {
				status.ResolvedActuals++
			} else if record.Actual != nil {
				status.UnresolvedActuals++
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		status.Issues = append(status.Issues, "prediction_read_failed")
	}
	if evaluation, err := s.Store.LatestEvaluation(ctx, evidenceDate); err == nil {
		status.Evaluation = &evaluation
		if !evaluation.Gate.Passed {
			status.Issues = append(status.Issues, "candidate_not_validated")
		}
	}
	if run, err := s.Store.LatestValidatedModelBefore(ctx, statusDate, maxModelAgeDays); err == nil {
		view := ModelView{ModelID: run.ModelID, Kind: run.Kind, Status: string(run.Status), FeatureVersion: run.FeatureVersion,
			CodeVersion: run.CodeVersion, TrainFrom: run.TrainFrom, TrainTo: run.TrainTo, CalibrationFrom: run.CalibrationFrom,
			CalibrationTo: run.CalibrationTo, ValidationFrom: run.ValidationFrom, ValidationTo: run.ValidationTo,
			ValidFor: run.ValidFor, Checksum: run.Checksum, CreatedAt: run.CreatedAt, Reason: run.Reason}
		status.ActiveModel = &view
	} else if errors.Is(err, sql.ErrNoRows) {
		view := baselineView(ModelRun{ModelID: BaselineModelID, Kind: "baseline", Status: ModelStatusValidated})
		status.ActiveModel = &view
		status.Issues = append(status.Issues, "validated_model_unavailable")
	} else {
		status.Issues = append(status.Issues, "model_read_failed")
	}
	return status, nil
}

// Predictions returns only saved forecasts. Dashboard requests never trigger
// training, data collection or reconciliation.
func (s *Service) Predictions(ctx context.Context, target, code string) ([]PredictionRecord, error) {
	if s == nil || s.Store == nil {
		return nil, errors.New("dayrange store is unavailable")
	}
	if target == "" {
		return nil, errors.New("dayrange target date is required")
	}
	return s.Store.LoadPredictions(ctx, target, code)
}

// Evaluation returns the latest saved candidate/baseline report for a completed
// audit date. It does not recompute metrics from dashboard requests.
func (s *Service) Evaluation(ctx context.Context, asof string) (Evaluation, error) {
	if s == nil || s.Store == nil {
		return Evaluation{}, errors.New("dayrange store is unavailable")
	}
	return s.Store.LatestEvaluation(ctx, asof)
}

type ReplayItem struct {
	Prediction Prediction `json:"prediction"`
	Replayed   Forecast   `json:"replayed"`
	Match      bool       `json:"match"`
}

func (s *Service) Replay(ctx context.Context, target, code string) ([]ReplayItem, error) {
	if s == nil || s.Store == nil {
		return nil, errors.New("dayrange service is incomplete")
	}
	if target == "" {
		target = calendar.Date(s.now())
	}
	records, err := s.Store.LoadPredictions(ctx, target, code)
	if err != nil {
		return nil, err
	}
	out := make([]ReplayItem, 0, len(records))
	for _, record := range records {
		var snapshot Snapshot
		if err := s.Store.LoadSnapshot(ctx, record.InputSnapshotID, &snapshot); err != nil {
			return nil, err
		}
		var forecast Forecast
		switch record.Source {
		case "baseline":
			if snapshot.Baseline == nil {
				return nil, errors.New("dayrange baseline snapshot is missing")
			}
			forecast = *snapshot.Baseline
		case "model":
			run, err := s.Store.LoadModelRun(ctx, record.ModelID)
			if err != nil {
				return nil, err
			}
			model, err := LoadArtifact(run.Artifact)
			if err != nil {
				return nil, err
			}
			forecast, err = model.Predict(snapshot.Values(), snapshot.ReferencePrice)
			if err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("dayrange prediction has an unknown source")
		}
		match := forecast.High.Point == record.PointHigh && forecast.Low.Point == record.PointLow && forecastEqual(forecast, record.Prediction)
		out = append(out, ReplayItem{Prediction: record.Prediction, Replayed: forecast, Match: match})
	}
	return out, nil
}

func forecastEqual(forecast Forecast, prediction Prediction) bool {
	if forecast.High.Interval == nil || forecast.Low.Interval == nil || prediction.IntervalHigh == nil || prediction.IntervalLow == nil {
		return false
	}
	return forecast.High.Interval.Lower == prediction.IntervalHigh.Lower && forecast.High.Interval.Upper == prediction.IntervalHigh.Upper &&
		forecast.Low.Interval.Lower == prediction.IntervalLow.Lower && forecast.Low.Interval.Upper == prediction.IntervalLow.Upper
}

func (s *Service) Run(ctx context.Context) error {
	if s == nil || !s.Config.DayRange.Enabled {
		return nil
	}
	ticker := time.NewTicker(runPollInterval)
	defer ticker.Stop()
	s.Log.Info("Dayrange monitor started", "train_at", s.Config.DayRange.TrainAt, "predict_at", s.Config.DayRange.PredictAt, "reconcile_at", s.Config.DayRange.ReconcileAt)
	for {
		if err := s.step(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.Log.Warn("Dayrange monitor step failed", "error", err.Error())
		}
		if err := s.Flush(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.Log.Warn("Dayrange delivery failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) step(ctx context.Context) error {
	now := s.now()
	cal := calendar.New(s.Config)
	today := calendar.Date(now)
	trading, err := cal.Trading(today)
	if err != nil || !trading {
		return nil
	}
	next, err := cal.Next(today)
	if err != nil {
		return err
	}
	predictAt, _ := time.ParseInLocation("15:04", s.Config.DayRange.PredictAt, shanghai)
	predictTime := time.Date(now.Year(), now.Month(), now.Day(), predictAt.Hour(), predictAt.Minute(), 0, 0, shanghai)
	openTime := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, shanghai)
	if !now.Before(predictTime) && now.Before(openTime) {
		records, err := s.Store.LoadPredictions(ctx, today, "")
		if err != nil {
			return err
		}
		if len(records) == 0 {
			if _, err := s.Predict(ctx, today, ""); err != nil {
				return err
			}
		} else if err := s.publishSavedPremarket(ctx, today, records, nil, now); err != nil {
			return err
		}
	}
	trainAt, _ := time.ParseInLocation("15:04", s.Config.DayRange.TrainAt, shanghai)
	trainTime := time.Date(now.Year(), now.Month(), now.Day(), trainAt.Hour(), trainAt.Minute(), 0, 0, shanghai)
	if !now.Before(trainTime) {
		if _, err := s.Store.LatestModelForValidFor(ctx, next); errors.Is(err, sql.ErrNoRows) {
			if _, err := s.Train(ctx, today, false); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	reconcileAt, _ := time.ParseInLocation("15:04", s.Config.DayRange.ReconcileAt, shanghai)
	reconcileTime := time.Date(now.Year(), now.Month(), now.Day(), reconcileAt.Hour(), reconcileAt.Minute(), 0, 0, shanghai)
	if !now.Before(reconcileTime) {
		records, err := s.Store.LoadPredictions(ctx, today, "")
		if err == nil {
			needsActual := false
			for _, record := range records {
				if record.Actual == nil || record.Actual.Status != "resolved" {
					needsActual = true
					break
				}
			}
			if needsActual {
				if _, err := s.Reconcile(ctx, today); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Service) loadData(ctx context.Context, through string) (MarketData, error) {
	end, err := time.ParseInLocation("2006-01-02", through, shanghai)
	if err != nil {
		return MarketData{}, errors.New("invalid dayrange data date")
	}
	from := end.AddDate(0, 0, -s.Config.DayRange.HistoryDays-180).Format("2006-01-02")
	return s.Market.LoadDayRangeData(ctx, s.Config.Stocks, from, through)
}

func (s *Service) completedAndNext(asof string, now time.Time) (string, string, error) {
	cal := calendar.New(s.Config)
	completed := asof
	if completed == "" {
		var err error
		completed, err = cal.Completed(now)
		if err != nil {
			return "", "", err
		}
	}
	if ok, err := cal.Trading(completed); err != nil || !ok {
		return "", "", errors.New("dayrange as-of must be a covered completed trading day")
	}
	latest, err := cal.Completed(now)
	if err != nil {
		return "", "", err
	}
	if completed > latest {
		return "", "", errors.New("dayrange as-of must be a completed trading day")
	}
	next, err := cal.Next(completed)
	if err != nil {
		return "", "", err
	}
	return completed, next, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().In(shanghai)
	}
	return time.Now().In(shanghai)
}

func (s *Service) invalidateModel(ctx context.Context, run ModelRun, cause error) error {
	run.Status = ModelStatusInvalid
	run.Reason = "artifact_invalid: " + cause.Error()
	if err := s.Store.SaveModelRun(ctx, run); err != nil {
		return fmt.Errorf("cannot invalidate dayrange model %s: %w", run.ModelID, err)
	}
	s.Log.Warn("Dayrange validated model marked invalid", "model_id", run.ModelID, "error", cause.Error())
	return nil
}

func baselineView(run ModelRun) ModelView {
	return ModelView{ModelID: run.ModelID, Kind: run.Kind, Status: string(run.Status), FeatureVersion: run.FeatureVersion,
		CodeVersion: run.CodeVersion, TrainFrom: run.TrainFrom, TrainTo: run.TrainTo, CalibrationFrom: run.CalibrationFrom,
		CalibrationTo: run.CalibrationTo, ValidationFrom: run.ValidationFrom, ValidationTo: run.ValidationTo,
		ValidFor: run.ValidFor, Checksum: run.Checksum, CreatedAt: run.CreatedAt, Reason: run.Reason}
}

func summaryMetrics(report Report) map[string]float64 {
	out := map[string]float64{}
	for _, metric := range report.Metrics {
		if !baseSlice(metric.Slice) || metric.Value == nil || metric.Aggregation != modeleval.AggregationDayEqual {
			continue
		}
		if metric.MetricID == modeleval.MetricPointMAE && (metric.Target == modeleval.TargetHigh || metric.Target == modeleval.TargetLow) && metric.Space == modeleval.SpacePrice {
			out[metric.MetricID+"."+metric.Target] = *metric.Value
		}
		if metric.MetricID == modeleval.MetricIntervalEmpiricalCoverage && (metric.Target == modeleval.TargetHigh || metric.Target == modeleval.TargetLow) && metric.Space == modeleval.SpaceNone {
			out[metric.MetricID+"."+metric.Target] = *metric.Value
		}
	}
	return out
}
