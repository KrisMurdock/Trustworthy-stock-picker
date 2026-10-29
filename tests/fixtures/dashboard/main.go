// Command dashboard creates deterministic, credential-free browser fixtures using
// the same SQLite transaction and schema as the production synchronizer.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/bottomstudy"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dayrange"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
	"math"
	"os"
	"time"
)

func main() {
	root := flag.String("data", "", "existing fixture data directory")
	empty := flag.Bool("empty", false, "initialize without published snapshots")
	partial := flag.Bool("partial", false, "save market data without a report after coverage failure")
	gates := flag.Bool("gates", false, "seed rejected strategy rules and an empty recommendation holdout")
	predictorFixture := flag.Bool("predictor", false, "seed an experimental minute predictor model, status and predictions")
	dayRangeFixture := flag.Bool("dayrange", false, "seed frozen premarket day-range predictions, actuals and evaluation")
	flag.Parse()
	if err := seed(*root, *empty, *partial, *gates); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !*empty {
		if err := seedExperiments(*root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if *predictorFixture {
			if err := seedPredictor(*root); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		if *dayRangeFixture {
			if err := seedDayRange(*root); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
}

func seedDayRange(root string) error {
	db, err := storage.InitDayRange(root, false)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 8, 45, 0, 0, market.Shanghai)
	target, ref := "2026-10-20", "2026-10-19"
	if err := db.SaveModelRun(ctx, dayrange.ModelRun{ModelID: dayrange.BaselineModelID, Kind: "baseline", Status: dayrange.ModelStatusValidated,
		FeatureVersion: dayrange.FeatureVersion, CodeVersion: "fixture", CreatedAt: now, TrainFrom: "2025-10-20", TrainTo: ref, ValidFor: target}); err != nil {
		return err
	}
	stocks := []config.Stock{{Code: "sz000001", Name: "测试银行", Sector: "银行"}, {Code: "sh600519", Name: "测试消费", Sector: "消费"}}
	var firstSnapshotID string
	for index, stock := range stocks {
		base := 10.25 + float64(index)*187.75
		high := dayrange.Estimate{Point: base * 1.05, Quantiles: []dayrange.Quantile{{Level: .1, Price: base * 1.03}, {Level: .5, Price: base * 1.05}, {Level: .9, Price: base * 1.08}},
			Interval: &dayrange.Interval{Lower: base * 1.03, Upper: base * 1.08, NominalCoverage: .8, Method: "trailing_quantile_60"}}
		low := dayrange.Estimate{Point: base * .93, Quantiles: []dayrange.Quantile{{Level: .1, Price: base * .91}, {Level: .5, Price: base * .93}, {Level: .9, Price: base * .96}},
			Interval: &dayrange.Interval{Lower: base * .91, Upper: base * .96, NominalCoverage: .8, Method: "trailing_quantile_60"}}
		baseline := dayrange.Forecast{High: high, Low: low}
		features := make([]dayrange.Feature, len(dayrange.FeatureNames()))
		for featureIndex, name := range dayrange.FeatureNames() {
			value := float64(featureIndex%5) / 100
			features[featureIndex] = dayrange.Feature{Name: name, Value: &value}
		}
		snapshot := dayrange.Snapshot{SchemaVersion: dayrange.SnapshotSchemaVersion, Code: stock.Code, Name: stock.Name, Sector: stock.Sector,
			TargetDate: target, Mode: dayrange.ModePremarket, AsOf: now, InputThrough: ref, ReferencePrice: base, ScalePrice: base * .015,
			FeatureVersion: dayrange.FeatureVersion, Features: features,
			Availability: dayrange.Availability{Stock: "available", Market: "available", Sector: "available"}, MarketState: "up", VolatilityBucket: "medium",
			Baseline: &baseline, Slices: []modeleval.SliceDimension{{Name: "code", Value: stock.Code}, {Name: "market_state", Value: "up"}, {Name: "volatility", Value: "medium"}}, CreatedAt: now}
		snapshotID, err := db.PutSnapshot(ctx, "prediction", stock.Code, target, dayrange.ModePremarket, now, snapshot)
		if err != nil {
			return err
		}
		if firstSnapshotID == "" {
			firstSnapshotID = snapshotID
		}
		prediction := dayrange.Prediction{Code: stock.Code, TargetDate: target, Mode: dayrange.ModePremarket, AsOf: now, InputThrough: ref,
			ModelID: dayrange.BaselineModelID, Source: "baseline", FeatureVersion: dayrange.FeatureVersion, ReferencePrice: base,
			PointHigh: high.Point, PointLow: low.Point, QuantilesHigh: high.Quantiles, QuantilesLow: low.Quantiles,
			IntervalHigh: high.Interval, IntervalLow: low.Interval, InputSnapshotID: snapshotID, BaselineModelID: dayrange.BaselineModelID,
			Status: "published", CreatedAt: now}
		if _, err := db.SavePrediction(ctx, prediction); err != nil {
			return err
		}
		actual := dayrange.ActualRecord{Code: stock.Code, TargetDate: target, ObservedThrough: target, ResolvedAt: now.Add(10 * time.Hour), Status: "resolved",
			Source: "tencent", Actual: &dayrange.Extremes{High: base * 1.06, Low: base * .94}}
		if index == 1 {
			actual.Actual, actual.Source, actual.Status, actual.Reason = nil, "", "unresolved", "target_status_unknown"
		}
		if err := db.SaveActual(ctx, actual); err != nil {
			return err
		}
	}
	audit := dayrange.Audit{SchemaVersion: dayrange.SnapshotSchemaVersion, AsOf: target, From: "2025-10-20", To: target, PoolSize: 2,
		TradingDays: 250, Candidates: 2, Resolved: 1, Unresolved: 1, Trainable: 1, UnresolvedReasons: map[string]int{"target_status_unknown": 1},
		TrainingReasons: map[string]int{}, FeatureAvailability: map[string]int{"stock:available": 2, "market:available": 2, "sector:available": 2},
		RawSources: map[string]int{"tencent": 1}, AdjustedSources: map[string]int{"tencent": 1}, LabelSources: map[string]int{"daily_bar:tencent": 1},
		Days: []dayrange.AuditDay{{Date: target, Resolved: 1, Unresolved: 1, Trainable: 1, Reasons: map[string]int{"target_status_unknown": 1}}}}
	if _, err := db.SaveAudit(ctx, firstSnapshotID, target, now, audit); err != nil {
		return err
	}
	candidateMetrics := []modeleval.MetricRecord{
		dayrangeFixtureMetric(modeleval.MetricPointMAE, modeleval.TargetHigh, modeleval.SpacePrice, .12, nil),
		dayrangeFixtureMetric(modeleval.MetricPointMAE, modeleval.TargetLow, modeleval.SpacePrice, .15, nil),
		dayrangeFixtureMetric(modeleval.MetricIntervalEmpiricalCoverage, modeleval.TargetHigh, modeleval.SpaceNone, .8, dayrangeCoverageSlice("model")),
		dayrangeFixtureMetric(modeleval.MetricIntervalEmpiricalCoverage, modeleval.TargetLow, modeleval.SpaceNone, .6, dayrangeCoverageSlice("model")),
		dayrangeFixtureMetric(modeleval.MetricIntervalWidthMean, modeleval.TargetHigh, modeleval.SpacePrice, .5, dayrangeCoverageSlice("model")),
		dayrangeFixtureMetric(modeleval.MetricIntervalWidthMean, modeleval.TargetLow, modeleval.SpacePrice, .6, dayrangeCoverageSlice("model")),
	}
	baselineMetrics := []modeleval.MetricRecord{
		dayrangeFixtureMetric(modeleval.MetricPointMAE, modeleval.TargetHigh, modeleval.SpacePrice, .2, nil),
		dayrangeFixtureMetric(modeleval.MetricPointMAE, modeleval.TargetLow, modeleval.SpacePrice, .22, nil),
		dayrangeFixtureMetric(modeleval.MetricIntervalWidthMean, modeleval.TargetHigh, modeleval.SpacePrice, .52, dayrangeCoverageSlice("baseline")),
		dayrangeFixtureMetric(modeleval.MetricIntervalWidthMean, modeleval.TargetLow, modeleval.SpacePrice, .55, dayrangeCoverageSlice("baseline")),
	}
	evaluation := dayrange.Evaluation{ModelID: "fixture-candidate", BaselineID: dayrange.BaselineModelID, Mode: dayrange.ModePremarket, AsOf: target,
		ValidationFrom: "2026-09-01", ValidationTo: "2026-10-19", MetricsVersion: dayrange.EvaluationSchemaVersion,
		Candidate: dayrange.Report{SchemaVersion: dayrange.EvaluationSchemaVersion, Subject: "fixture-candidate", Status: "ok", Resolved: 260, Metrics: candidateMetrics},
		Baseline:  dayrange.Report{SchemaVersion: dayrange.EvaluationSchemaVersion, Subject: dayrange.BaselineModelID, Status: "ok", Resolved: 260, Metrics: baselineMetrics},
		Gate: dayrange.Gate{Passed: false, Reasons: []string{"marginal_coverage_outside_75_85"}, HighMAE: .12, LowMAE: .15,
			HighCoverage: .8, LowCoverage: .6, HighWidthRatio: .96, LowWidthRatio: 1.09, JointCoverage: .72,
			ValidationDays: 20, ValidationSamples: 260}, CreatedAt: now}
	_, err = db.SaveEvaluation(ctx, evaluation)
	return err
}

func dayrangeFixtureMetric(id, target string, space modeleval.Space, value float64, extra []modeleval.SliceDimension) modeleval.MetricRecord {
	slice := []modeleval.SliceDimension{{Name: "as_of", Value: "08:45"}, {Name: "mode", Value: "premarket"}}
	slice = append(slice, extra...)
	unit := modeleval.UnitCNY
	if space == modeleval.SpaceNone {
		unit = modeleval.UnitRatio
	}
	return modeleval.MetricRecord{MetricID: id, Family: modeleval.FamilyInterval, Role: modeleval.RoleGuardrail, Subject: "fixture",
		Target: target, Space: space, Unit: unit, Aggregation: modeleval.AggregationDayEqual, Value: modeleval.Float(value),
		Applicability: modeleval.ApplicabilityOK, Samples: 260, Days: 20, Stocks: 2, Slice: slice}
}

func dayrangeCoverageSlice(method string) []modeleval.SliceDimension {
	return []modeleval.SliceDimension{{Name: "coverage", Value: "0.8"}, {Name: "interval_method", Value: method}}
}

func seedPredictor(root string) error {
	now := time.Now().In(market.Shanghai)
	date := now.Format("2006-01-02")
	day, err := time.ParseInLocation("2006-01-02", date, market.Shanghai)
	if err != nil {
		return err
	}
	input := day.Add(9*time.Hour + 35*time.Minute)
	target := input.Add(5 * time.Minute)
	info := predictor.ModelInfo{
		SchemaVersion: predictor.ArtifactSchemaVersion, CreatedAt: now.UTC(), DataFrom: "2026-09-18", DataTo: date,
		ValidFor: date, FeatureVersion: predictor.FeatureVersion, FeatureNames: predictor.FeatureNames(), CodeVersion: "fixture",
		Metrics: fixtureMetricRecords("model:candidate"),
	}
	artifact := &predictor.Artifact{
		SchemaVersion: predictor.ArtifactSchemaVersion, Info: info,
		Hyperparameters: predictor.DefaultTrainOptions().Hyperparameters,
		Base:            map[predictor.Target]float64{predictor.TargetHigh: 0.004, predictor.TargetLow: -0.004, predictor.TargetClose: 0},
		Trees:           map[predictor.Target][]predictor.Tree{predictor.TargetHigh: {}, predictor.TargetLow: {}, predictor.TargetClose: {}},
		LabelClip:       map[predictor.Target]float64{predictor.TargetHigh: 0.05, predictor.TargetLow: 0.05, predictor.TargetClose: 0.05},
	}
	sealed, err := predictor.SealArtifact(artifact)
	if err != nil {
		return err
	}
	artifact.Info.Metrics = fixtureMetricRecords(modeleval.ModelSubject(artifact.Info.ModelID))
	sealed, err = predictor.ResealArtifact(artifact)
	if err != nil {
		return err
	}
	predictions, err := predictor.OpenDB(root)
	if err != nil {
		return err
	}
	defer predictions.Close()
	for index, code := range []string{"sz000001", "sh600519"} {
		base := 10.25 + float64(index)*187.75
		if _, err := predictions.InsertPrediction(context.Background(), predictor.Prediction{
			Code: code, Date: date, InputEnd: input, TargetEnd: target, ModelID: artifact.Info.ModelID, BasePrice: base,
			HighPct: 0.006, LowPct: -0.004, ClosePct: 0.002, Trend: "偏强", Confidence: "中", TrendScore: 1.2, CreatedAt: now.UTC(),
		}); err != nil {
			return err
		}
	}
	store, err := storage.Open(root, false)
	if err != nil {
		return err
	}
	defer store.Close()
	modelPath := "models/intraday/" + artifact.Info.ModelID + "/model.json"
	if err := store.Write(modelPath, json.RawMessage(sealed)); err != nil {
		return err
	}
	if err := store.Write("models/intraday/current.json", predictor.ModelPointer{
		SchemaVersion: 1, ModelID: artifact.Info.ModelID, Path: modelPath, Checksum: artifact.Info.Checksum, ValidFor: date, CreatedAt: artifact.Info.CreatedAt,
	}); err != nil {
		return err
	}
	if err := predictor.WriteStatus(store, predictor.Status{
		Version: predictor.StatusSchemaVersion, Enabled: true, PushEnabled: true, ModelID: artifact.Info.ModelID,
		ModelValidFor: date, TrainedAt: artifact.Info.CreatedAt, LastPredictionAt: now.UTC(), LastPredictionEnd: target,
		PredictionsToday: 2, UpdatedAt: now.UTC(),
	}); err != nil {
		return err
	}
	return predictor.WriteRuntimeConfig(store, predictor.RuntimeConfig{PushEnabled: true})
}

func seed(root string, empty, partial, gates bool) error {
	db, err := storage.InitMarket(root, false)
	if err != nil {
		return err
	}
	defer db.Close()
	if empty {
		return nil
	}
	stocks := []config.Stock{
		{Code: "sz000001", Name: "测试银行", Sector: "银行"},
		{Code: "sh600519", Name: "测试消费", Sector: "消费"},
		{Code: "sz000002", Name: "测试缺失", Sector: "地产"},
		{Code: "sz000004", Name: "测试停牌", Sector: "运输"},
		{Code: "sz000005", Name: "测试缺口", Sector: "工业"},
	}
	for _, stock := range []config.Stock{stocks[0], stocks[2]} {
		if err := seedMinutes(db, stock); err != nil {
			return err
		}
	}
	for _, day := range []string{"2026-10-12", "2026-10-13"} {
		at, err := time.ParseInLocation("2006-01-02", day, market.Shanghai)
		if err != nil {
			return err
		}
		at = at.Add(19 * time.Hour)
		items := []storage.SyncItem{}
		for i, stock := range stocks[:2] {
			adjusted := history(stock, day, at, i, 80)
			raw := history(stock, day, at, i, 2700)
			if i == 1 {
				adjusted.Source, raw.Source = "baostock", "baostock"
			}
			raw.Adjust, raw.DataField = "none", "day"
			for j := range raw.Bars {
				bar := &raw.Bars[j]
				bar.Open, bar.Close, bar.High, bar.Low = bar.Open*2, bar.Close*2, bar.High*2, bar.Low*2
			}
			items = append(items, storage.SyncItem{Stock: stock, Adjusted: &adjusted, Raw: &raw, Status: "ok"})
		}
		items = append(items, storage.SyncItem{Stock: stocks[2], Status: "isolated", Reason: "missing_data"})
		items = append(items, storage.SyncItem{Stock: stocks[3], Status: "isolated", Reason: "complete_not_analyzable", Sessions: 80, Suspended: 1, Complete: true})
		gapAdjusted, gapRaw := history(stocks[4], day, at, 0, 80), history(stocks[4], day, at, 0, 2700)
		gapRaw.Adjust, gapRaw.DataField = "none", "day"
		items = append(items, storage.SyncItem{Stock: stocks[4], Adjusted: &gapAdjusted, Raw: &gapRaw, Status: "ok", RawStatus: "ok", Sessions: 80, Gaps: 1, Analyzable: true, RawAnalyzable: true})
		report := selection.Report{
			Version: 1, ID: "dashboard-" + day, AsOf: day, Target: at.AddDate(0, 0, 1).Format("2006-01-02"),
			GeneratedAt: at, Strategy: "leader_daily_v1", SelectionEnabled: true, Adjust: "qfq",
			TopN: 5, PoolSize: 5, Eligible: 3, Passed: 1,
			Rules:    []string{"浏览器测试快照"},
			Selected: []selection.Candidate{{Code: stocks[1].Code, Name: stocks[1].Name, Sector: stocks[1].Sector, Close: 198, ChangePct: -1, Metrics: selection.Metrics{Score: 77}}},
			Excluded: []selection.Excluded{{Code: stocks[2].Code, Reason: "missing_data"}}, Filtered: []selection.Excluded{},
		}
		if day == "2026-10-13" {
			report.Version, report.Strategy = 2, "short_horizon_v1"
			report.Ranking = []strategy.Candidate{
				{Code: stocks[0].Code, Name: stocks[0].Name, Rank: 1, Score: 88.5, Recommended: true, Reasons: []string{"证据充分"}, Risks: []string{}, Rules: []string{"trend"}, Continuation: &strategy.ContinuationStats{Version: 1, Label: strategy.ContinuationLabel, Rule: "momentum", Samples: 120, Dates: 40, Hits: 66, Rate: .55, LowerBound: .46, Score: 46, Status: "ready"}},
				{Code: stocks[1].Code, Name: stocks[1].Name, Rank: 2, Score: 62.5, Recommended: false, Reasons: []string{"历史样本不足"}, Risks: []string{"evidence"}, Rules: []string{}, Continuation: &strategy.ContinuationStats{Version: 1, Label: strategy.ContinuationLabel, Rule: "baseline", Status: "not_applicable"}},
			}
			report.Selected = []selection.Candidate{{Code: stocks[0].Code, Name: stocks[0].Name, Sector: stocks[0].Sector, Close: 10.25, ChangePct: 2.5, Metrics: selection.Metrics{Score: 85}, Ranking: &report.Ranking[0]}}
			report.Research = &strategy.Research{Version: 1, Strategy: report.Strategy, AsOf: day, SampleCount: 120, ValidationPassed: false, Limitations: []string{"仅用于浏览器测试"}}
			if gates {
				report.Passed, report.Selected = 0, nil
				report.Ranking[0].Recommended = false
				report.Ranking[0].Rules = []string{"baseline", "momentum"}
				report.Ranking[0].Reasons = []string{"rising_ma20"}
				report.Ranking[0].Risks = []string{"historical_net_loss_limit_breached", "insufficient_or_unprofitable_evidence", "holdout_validation_not_passed"}
				report.Ranking[1].Rules = []string{"baseline"}
				report.Ranking[1].Reasons = []string{"rising_ma20"}
				report.Ranking[1].Risks = []string{"insufficient_or_unprofitable_evidence", "holdout_validation_not_passed"}
				report.Research.Parameters = config.DefaultStrategy()
				report.Research.Holdout = strategy.Evaluation{Method: "frozen_holdout", From: "2026-08-06", Through: "2026-10-08", SignalDates: 40}
				report.Research.Rules = []strategy.Stats{
					{Rule: "baseline", Target: 3, Horizon: 5, HitRate: .458},
					{Rule: "momentum", Target: 3, Horizon: 5, Samples: 1095, Dates: 154, Resolved: 1041, Unresolved: 54, HitRate: .474, MeanNet: .36, WorstNet: -15.3766, LossBreaches: 209, LossLimitPct: 3},
				}
			}
		}
		if partial {
			if day != "2026-10-13" {
				continue
			}
			return db.CommitMarketData(context.Background(), storage.SyncRun{ID: "partial-run", AsOf: day, StartedAt: at.Add(-time.Minute), FinishedAt: at, CoveragePct: 200.0 / 3}, items, "qfq")
		}
		if err := db.CommitSnapshot(context.Background(), storage.SyncRun{ID: "dashboard-run-" + day, AsOf: day, StartedAt: at.Add(-time.Minute), FinishedAt: at, CoveragePct: 200.0 / 3}, items, report); err != nil {
			return err
		}
	}
	at, _ := time.ParseInLocation("2006-01-02", "2026-10-13", market.Shanghai)
	at = at.Add(19 * time.Hour)
	suspended := stocks[3]
	if err := db.SaveStatusWindow(context.Background(), suspended, market.StatusWindow{
		Code: suspended.Code, Start: "2026-10-13", End: "2026-10-13", Source: "baostock", FetchedAt: at, Full: true,
		Statuses: []market.SessionStatus{{Code: suspended.Code, Date: "2026-10-13", State: market.SessionSuspended, Source: "baostock", FetchedAt: at}},
	}); err != nil {
		return err
	}
	return seedMarketContext(root)
}

func history(stock config.Stock, day string, at time.Time, index, count int) market.History {
	var bars []market.Bar
	for date := at; len(bars) < count; date = date.AddDate(0, 0, -1) {
		if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
			continue
		}
		close := 10.0
		if index == 1 {
			close = 200
		}
		if len(bars) == 0 {
			if index == 0 {
				close = 10.25
			} else {
				close = 198
			}
		}
		bars = append(bars, market.Bar{Date: date.Format("2006-01-02"), Open: close * (0.99 + .02*float64(index)), Close: close, High: close * 1.01, Low: close * .99, Volume: float64((index + 1) * 1000)})
	}
	for left, right := 0, len(bars)-1; left < right; left, right = left+1, right-1 {
		bars[left], bars[right] = bars[right], bars[left]
	}
	return market.History{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: day, FetchedAt: at, Bars: bars}
}

func fixtureMetricRecords(subject string) []modeleval.MetricRecord {
	records := []modeleval.MetricRecord{}
	for _, target := range []string{modeleval.TargetHigh, modeleval.TargetLow, modeleval.TargetClose} {
		modelValue := 0.004
		if target == modeleval.TargetClose {
			modelValue = 0.003
		}
		records = append(records, modeleval.MetricRecord{
			MetricID: modeleval.MetricPointMAE, Family: modeleval.FamilyPoint, Role: modeleval.RolePrimary,
			Subject: subject, Target: target, Space: modeleval.SpaceReturn, Unit: modeleval.UnitBPS,
			Aggregation: modeleval.AggregationDayEqual, Value: modeleval.Float(modelValue * 10000),
			Applicability: modeleval.ApplicabilityOK, Samples: 640, Days: 4, Stocks: 2, HigherIsBetter: false,
		}, modeleval.MetricRecord{
			MetricID: modeleval.MetricPointMAE, Family: modeleval.FamilyPoint, Role: modeleval.RoleDiagnostic,
			Subject: modeleval.BaselineSubject(modeleval.BaselineLastPrice), Target: target, Space: modeleval.SpaceReturn,
			Unit: modeleval.UnitBPS, Aggregation: modeleval.AggregationDayEqual, Value: modeleval.Float(0.005 * 10000),
			Applicability: modeleval.ApplicabilityOK, Samples: 640, Days: 4, Stocks: 2, HigherIsBetter: false,
		})
	}
	return records
}

func seedExperiments(root string) error {
	ctx := context.Background()
	db, err := storage.OpenExperiments(ctx, root, false, true)
	if err != nil {
		return err
	}
	defer db.Close()
	c := config.Defaults()
	c.Calendar.ValidFrom = "2025-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试银行", Sector: "银行"}, {Code: "sh600519", Name: "测试消费", Sector: "消费"}, {Code: "sz000002", Name: "测试缺失", Sector: "地产"}}
	at, _ := time.Parse("2006-01-02", "2026-10-13")
	adjusted, raw := []market.History{}, []market.History{}
	for index, stock := range c.Stocks[:2] {
		h := history(stock, "2026-10-13", at, index, 240)
		for i := range h.Bars {
			p := 20 + float64(i)*.04 + .2*math.Sin(float64(i)*.4)
			b := &h.Bars[i]
			b.Open = p * .995
			b.Close = p
			b.Low = p * .99
			b.High = p * 1.003
			if index == 0 {
				b.High = p * 1.03
			}
		}
		r := h
		r.Adjust, r.DataField = "none", "day"
		r.Bars = append([]market.Bar(nil), h.Bars...)
		for i := range r.Bars {
			b := &r.Bars[i]
			b.Open *= 2
			b.High *= 2
			b.Low *= 2
			b.Close *= 2
		}
		adjusted = append(adjusted, h)
		raw = append(raw, r)
	}
	snapshot, err := experiment.NewSnapshot(c, "2026-10-13", adjusted, raw)
	if err != nil {
		return err
	}
	var parent *experiment.Record
	for _, policy := range []string{"baseline", "upper-wick", "next-day-baseline"} {
		spec, _ := experiment.Specification(policy)
		r, err := experiment.NewRecord(snapshot, spec, parent, "fixture-v1")
		if err != nil {
			return err
		}
		if err = db.Start(ctx, snapshot, r); err != nil {
			return err
		}
		if err = experiment.Evaluate(ctx, snapshot, &r, parent); err != nil {
			return err
		}
		if err = db.Finish(ctx, r); err != nil {
			return err
		}
		parent = &r
	}
	minutes := []market.MinuteHistory{}
	for _, h := range raw {
		m := market.MinuteHistory{Version: 1, Code: h.Code, Name: h.Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: at.Add(16 * time.Hour)}
		for _, b := range h.Bars[len(h.Bars)-13:] {
			day, _ := time.ParseInLocation("2006-01-02", b.Date, market.Shanghai)
			for i := 0; i < 48; i++ {
				minute := 575 + i*5
				if i >= 24 {
					minute = 785 + (i-24)*5
				}
				bar := market.MinuteBar{End: day.Add(time.Duration(minute) * time.Minute), Open: b.Open, Close: b.Open, High: b.Open, Low: b.Open, Volume: 100, Final: true}
				if i == 0 {
					bar.Low = b.Low
					bar.High = b.High
					bar.Close = b.Low + .75*(b.Open-b.Low)
				}
				if i == 1 {
					bar.Open = b.Low + .75*(b.Open-b.Low)
					bar.Low = bar.Open
				}
				if i == 47 {
					bar.Close = b.Close
					bar.High = max(bar.High, b.Close)
					bar.Low = min(bar.Low, b.Close)
				}
				m.Bars = append(m.Bars, bar)
			}
		}
		minutes = append(minutes, m)
	}
	snapshot, err = experiment.WithMinutes(snapshot, minutes)
	if err != nil {
		return err
	}
	spec, _ := experiment.Specification("conditional-entry")
	spec.Range = &strategy.BacktestRange{From: raw[0].Bars[len(raw[0].Bars)-14].Date, Through: raw[0].Bars[len(raw[0].Bars)-5].Date}
	r, err := experiment.NewRecord(snapshot, spec, nil, "fixture-v1")
	if err != nil {
		return err
	}
	if err = db.Start(ctx, snapshot, r); err != nil {
		return err
	}
	if err = experiment.Evaluate(ctx, snapshot, &r, nil); err != nil {
		return err
	}
	if err = db.Finish(ctx, r); err != nil {
		return err
	}
	contrast, _ := experiment.Specification("conditional-guard")
	contrast.Range = spec.Range
	child, err := experiment.NewRecord(snapshot, contrast, &r, "fixture-v1")
	if err != nil {
		return err
	}
	if err = db.Start(ctx, snapshot, child); err != nil {
		return err
	}
	if err = experiment.Evaluate(ctx, snapshot, &child, &r); err != nil {
		return err
	}
	if err = db.Finish(ctx, child); err != nil {
		return err
	}
	return seedResultSnapshot(ctx, db, snapshot)
}

func seedResultSnapshot(ctx context.Context, db *storage.ExperimentDB, data experiment.Snapshot) error {
	spec := experiment.Spec{Title: "测试无止损 · 持仓时间结果", Direction: "验证结果快照", Change: "两组固定参数对照", Criteria: "训练选优，后段复查", Range: &strategy.BacktestRange{From: "2026-01-01", Through: "2026-10-12"}}
	r, err := experiment.NewRecord(data, spec, nil, "fixture-v1")
	if err != nil {
		return err
	}
	r.Engine = bottomstudy.SpeedEngine
	if err = db.Start(ctx, data, r); err != nil {
		return err
	}
	s := bottomstudy.SpeedStats{ComparisonStats: bottomstudy.ComparisonStats{TradeStats: bottomstudy.TradeStats{Samples: 19, Entries: 19, Resolved: 19, Targets: 18, Rate: 18.0 / 19, MeanDays: 7.37, MeanNet: 2.55, WorstNet: -8.21}}, SuccessDays: 6.67, PenalizedDays: 7.42}
	p := bottomstudy.SpeedPair{ID: "fixture-pair", Policy: bottomstudy.TradePolicy{Rule: "macd_contraction", Batches: 2, Horizon: 20, Target: 3}, Confirmation: "none", Stock: bottomstudy.SpeedArm{Training: s, Later: s}, Combined: bottomstudy.SpeedArm{Training: s, Later: s}}
	report := bottomstudy.SpeedReport{Pairs: []bottomstudy.SpeedPair{p}, Winners: []bottomstudy.SpeedWinner{{ID: p.ID}}}
	now := time.Now().UTC()
	r.FinishedAt, r.Status, r.Verdict = &now, "completed", "research"
	r.ResultSnapshot = bottomstudy.SpeedResultSnapshot(report, r)
	return db.Finish(ctx, r)
}

func seedMinutes(db *storage.MarketDB, stock config.Stock) error {
	at := time.Date(2026, 10, 13, 15, 10, 0, 0, market.Shanghai)
	h := market.MinuteHistory{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: at}
	for day := at.AddDate(0, 0, -11); !day.After(at); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		for _, start := range []int{570, 780} {
			for n := start + 5; n <= start+120; n += 5 {
				end := time.Date(day.Year(), day.Month(), day.Day(), n/60, n%60, 0, 0, market.Shanghai)
				h.Bars = append(h.Bars, market.MinuteBar{End: end, Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, Final: true})
			}
		}
	}
	if err := db.SaveMinutes(context.Background(), stock, h.Bars[len(h.Bars)-1].End, at, &h, "ok", "", true); err != nil {
		return err
	}
	h.Bars = nil
	h.FetchedAt = time.Date(2026, 10, 14, 10, 1, 0, 0, market.Shanghai)
	for n := 575; n <= 605; n += 5 {
		end := time.Date(2026, 10, 14, n/60, n%60, 0, 0, market.Shanghai)
		h.Bars = append(h.Bars, market.MinuteBar{End: end, Open: 10, High: 12, Low: 9, Close: 11.5, Volume: 200, Final: end.Before(h.FetchedAt)})
	}
	return db.SaveMinutes(context.Background(), stock, h.Bars[len(h.Bars)-1].End, h.FetchedAt, &h, "ok", "", false)
}

func seedMarketContextMinutes(bars []storage.ContextBar, offset int) []market.MinuteBar {
	out := make([]market.MinuteBar, 0, len(bars)*48)
	for index := 1; index < len(bars); index++ {
		day, _ := time.ParseInLocation("2006-01-02", bars[index].Date, market.Shanghai)
		prevClose := bars[index-1].Close
		morningClose := prevClose * (1 + math.Sin(float64(index+offset)/6)*0.002)
		close := morningClose * (1 + math.Cos(float64(index+offset)/8)*0.002)
		for _, start := range []int{570, 780} {
			for minute := start + 5; minute <= start+120; minute += 5 {
				end := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, market.Shanghai)
				progress := float64(minute-start) / 120
				price := prevClose + (morningClose-prevClose)*progress
				if start == 780 {
					price = morningClose + (close-morningClose)*progress
				}
				out = append(out, market.MinuteBar{End: end, Open: price * 0.9995, High: price * 1.0005, Low: price * 0.999, Close: price, Volume: 1000, Final: true})
			}
		}
	}
	return out
}

func seedMarketContextSessionMinutes(minutes []market.MinuteBar) []market.MinuteBar {
	out := make([]market.MinuteBar, 0, len(minutes)/6)
	chunk := make([]market.MinuteBar, 0, 6)
	flush := func() {
		if len(chunk) != 6 {
			return
		}
		bar := market.MinuteBar{
			End: chunk[len(chunk)-1].End, Open: chunk[0].Open, High: chunk[0].High, Low: chunk[0].Low,
			Close: chunk[len(chunk)-1].Close, Volume: 0, Final: true,
		}
		for _, item := range chunk {
			if item.High > bar.High {
				bar.High = item.High
			}
			if item.Low < bar.Low {
				bar.Low = item.Low
			}
			bar.Volume += item.Volume
		}
		out = append(out, bar)
	}
	for _, bar := range minutes {
		if len(chunk) > 0 && (!bar.End.Truncate(24*time.Hour).Equal(chunk[0].End.Truncate(24*time.Hour)) || bar.End.Hour()*60+bar.End.Minute() == 785) {
			flush()
			chunk = chunk[:0]
		}
		chunk = append(chunk, bar)
		if len(chunk) == 6 {
			flush()
			chunk = chunk[:0]
		}
	}
	return out
}

func seedMarketContext(root string) error {
	db, err := storage.InitContext(root, false)
	if err != nil {
		return err
	}
	defer db.Close()
	items := []storage.ContextInstrument{
		{Code: "sh000001", Name: "上证指数", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "sz399001", Name: "深证成指", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "sz399006", Name: "创业板指", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "sh000300", Name: "沪深300", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "sh000688", Name: "科创50", Kind: "index", Category: "index", Source: "tencent"},
		{Code: "BK1036", Name: "半导体", Kind: "board", Category: "industry,concept", Source: "eastmoney"},
		{Code: "BK1137", Name: "存储芯片", Kind: "board", Category: "concept", Source: "eastmoney"},
		{Code: "BK1152", Name: "高带宽内存", Kind: "board", Category: "concept", Source: "eastmoney"},
	}
	at := time.Date(2026, 10, 13, 19, 0, 0, 0, market.Shanghai)
	for index, item := range items {
		bars := []storage.ContextBar{}
		close := 1000.0 + float64(index)*137
		for day := at.AddDate(0, 0, -330); !day.After(at); day = day.AddDate(0, 0, 1) {
			if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
				continue
			}
			close *= 1 + 0.004*math.Sin(float64(index+len(bars)))
			bars = append(bars, storage.ContextBar{Code: item.Code, Date: day.Format("2006-01-02"), Open: close * .995, Close: close, High: close * 1.01, Low: close * .99, Volume: 1000 + float64(index*100), Amount: close * 1000})
		}
		item.UpdatedAt = at
		if err := db.SaveInstrument(context.Background(), item, bars); err != nil {
			return err
		}
		if item.Code == "sh000001" || item.Code == "sz399006" {
			minutes := seedMarketContextMinutes(bars, index)
			if err := db.SaveMinutes(context.Background(), item.Code, minutes); err != nil {
				return err
			}
			if err := db.SaveSessionMinutes(context.Background(), item.Code, seedMarketContextSessionMinutes(minutes)); err != nil {
				return err
			}
		}
	}
	for offset, code := range []string{market.KoreaKOSPI, market.KoreaKOSDAQ} {
		close := 2500.0 + float64(offset)*300
		koreaBars := make([]market.KoreaBar, 0, 260)
		for day := at.AddDate(0, 0, -330); !day.After(at); day = day.AddDate(0, 0, 1) {
			if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
				continue
			}
			close *= 1 + 0.003*math.Sin(float64(offset+len(koreaBars))/9)
			koreaBars = append(koreaBars, market.KoreaBar{
				Date: day.Format("2006-01-02"), Open: close * .997, Close: close,
				High: close * 1.008, Low: close * .992, Volume: 1000 + float64(offset*100),
			})
		}
		if err := db.SaveKoreaBars(context.Background(), code, koreaBars); err != nil {
			return err
		}
	}
	hangSeng := make([]market.HangSengBar, 0, 260)
	close := 19000.0
	for day := at.AddDate(0, 0, -330); !day.After(at); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		close *= 1 + 0.0025*math.Sin(float64(len(hangSeng))/7)
		hangSeng = append(hangSeng, market.HangSengBar{
			Date: day.Format("2006-01-02"), Open: close * .997, Close: close,
			High: close * 1.008, Low: close * .992, Volume: 1000,
		})
	}
	if err := db.SaveHangSengBars(context.Background(), hangSeng); err != nil {
		return err
	}
	return db.SaveSyncStatus(context.Background(), storage.ContextSyncStatus{AsOf: "2026-10-13", StartedAt: at.Add(-time.Minute), FinishedAt: at, Status: "complete", Total: len(items), Succeeded: len(items)})
}
