package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type predictorEvalMeta struct {
	report selection.Report
	source string
	stocks []config.Stock
	codes  []string
}

const (
	evalUniverseReport           = "report"
	evalUniverseConfigDiagnostic = "config-diagnostic"
)

func runPredictorEval(ctx context.Context, c config.Config, asof, windowsArg string, validationDays, halfLife int, allowLateReports, baselineOnly bool, universe string, calibrationGrid bool, maxStocks, maxTrainingDays int, dumpPredictions string, marketContext bool, trainOptions predictor.TrainOptions) error {
	if universe == "" {
		universe = evalUniverseReport
	}
	if universe != evalUniverseReport && universe != evalUniverseConfigDiagnostic {
		return errors.New("predictor-eval universe must be report or config-diagnostic")
	}
	windows := []predictor.WindowSpec{}
	var err error
	if !baselineOnly {
		windows, err = parsePredictorWindows(windowsArg)
		if err != nil {
			return err
		}
	}
	if validationDays < 1 || validationDays > 60 {
		return errors.New("predictor-eval validation-days must be between 1 and 60")
	}
	if halfLife < 1 || halfLife > 365 {
		return errors.New("predictor-eval half-life must be between 1 and 365")
	}
	if maxStocks < 0 || maxStocks > 5000 {
		return errors.New("predictor-eval max-stocks must be between 0 and 5000")
	}
	if maxTrainingDays < 0 || maxTrainingDays > 250 {
		return errors.New("predictor-eval max-training-days must be between 0 and 250")
	}
	var predictionFile *os.File
	if dumpPredictions != "" {
		file, err := os.OpenFile(dumpPredictions, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open prediction dump: %w", err)
		}
		predictionFile = file
		defer predictionFile.Close()
	}
	if !c.Predictor.Enabled {
		return errors.New("predictor is disabled")
	}
	cal := calendar.New(c)
	if asof == "" {
		asof, err = cal.Completed(time.Now())
		if err != nil {
			return err
		}
	}
	if ok, err := cal.Trading(asof); err != nil || !ok {
		return errors.New("predictor-eval requires a completed trading day")
	}
	tradingDays, err := cal.TradingDays(c.Calendar.ValidFrom, asof)
	if err != nil {
		return err
	}
	requiredDays := validationDays + c.Predictor.MinTrainingDays
	if !baselineOnly {
		requiredDays = validationDays + windows[len(windows)-1].Days
	}
	if len(tradingDays) < requiredDays {
		report, err := predictor.EvaluateRollingWindows(ctx, nil, emptyEvalProvider, predictor.EvaluationOptions{
			AsOf: asof, Windows: windows, BaselineOnly: baselineOnly, CalibrationGrid: calibrationGrid, TradingDays: tradingDays,
			MinTrainingDays: c.Predictor.MinTrainingDays, MaxTrainingDays: maxTrainingDays, HalfLifeDays: halfLife,
			TrainOptions: trainOptions,
		})
		if err != nil {
			return err
		}
		report.Universe = universe
		report.Notes = append(report.Notes, fmt.Sprintf("insufficient calendar history: need %d trading days, have %d", requiredDays, len(tradingDays)))
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	startValidation := len(tradingDays) - validationDays
	// B0 is an expanding-history baseline, so load every trading day covered by
	// the configured calendar. Rolling-window models still only use the fixed
	// number of sessions supplied through -windows.
	historyStart := tradingDays[0]
	candidateDates := tradingDays[startValidation:]

	marketDB, err := storage.OpenMarketView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer marketDB.Close()
	if !marketDB.HasDatabase() {
		return errors.New("market database is not initialized; run stock-watcher init")
	}

	notes := []string{}
	metas := map[string]predictorEvalMeta{}
	codes := map[string]bool{}
	if universe == evalUniverseConfigDiagnostic {
		stocks := limitDiagnosticStocks(append([]config.Stock(nil), c.Stocks...), maxStocks)
		sort.Slice(stocks, func(i, j int) bool { return stocks[i].Code < stocks[j].Code })
		dayCodes := make([]string, len(stocks))
		for i, stock := range stocks {
			dayCodes[i] = stock.Code
			codes[stock.Code] = true
		}
		for _, date := range candidateDates {
			metas[date] = predictorEvalMeta{source: evalUniverseConfigDiagnostic, stocks: stocks, codes: dayCodes}
		}
		notes = append(notes, "config-diagnostic universe uses the current configured pool for every validation day; it is not formal point-in-time acceptance")
	} else {
		for _, date := range candidateDates {
			report, source, err := predictor.LoadPointInTimeReport(ctx, marketDB, date, cal, allowLateReports)
			if errors.Is(err, fs.ErrNotExist) {
				notes = append(notes, "no point-in-time report for "+date)
				continue
			}
			if err != nil {
				return err
			}
			topN := report.TopN
			if topN <= 0 {
				topN = c.Selection.TopN
			}
			stocks, observed := predictor.ReportUniverse(report, c.Stocks, topN)
			if len(stocks) == 0 {
				notes = append(notes, "empty evaluation universe for "+date)
				continue
			}
			if observed {
				notes = append(notes, "observation ranking used for "+date)
			}
			if source != "archive" {
				if source == "archive-late" {
					notes = append(notes, "late-generated archive report used for "+date+"; as_of is before target")
				} else if source == "selection_run-late" {
					notes = append(notes, "late-generated mutable selection_run report used for "+date+"; as_of is before target")
				} else {
					notes = append(notes, "mutable selection_run report used for "+date)
				}
			}
			dayCodes := make([]string, 0, len(stocks))
			for _, stock := range stocks {
				dayCodes = append(dayCodes, stock.Code)
				codes[stock.Code] = true
			}
			metas[date] = predictorEvalMeta{report: report, source: source, stocks: stocks, codes: dayCodes}
		}
	}
	if len(metas) == 0 {
		report, err := predictor.EvaluateRollingWindows(ctx, nil, emptyEvalProvider, predictor.EvaluationOptions{
			AsOf: asof, Windows: windows, BaselineOnly: baselineOnly, CalibrationGrid: calibrationGrid, TradingDays: tradingDays,
			MinTrainingDays: c.Predictor.MinTrainingDays, MaxTrainingDays: maxTrainingDays, HalfLifeDays: halfLife,
			TrainOptions: trainOptions,
		})
		if err != nil {
			return err
		}
		report.Universe = universe
		report.Notes = append(report.Notes, notes...)
		return json.NewEncoder(os.Stdout).Encode(report)
	}

	codeList := make([]string, 0, len(codes))
	for code := range codes {
		codeList = append(codeList, code)
	}
	sort.Strings(codeList)
	start, err := time.ParseInLocation("2006-01-02", historyStart, market.Shanghai)
	if err != nil {
		return err
	}
	through, err := time.ParseInLocation("2006-01-02", asof, market.Shanghai)
	if err != nil {
		return err
	}
	minutes, err := marketDB.LoadMinuteTrainingBars(ctx, codeList, start, through.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	daily, err := marketDB.LoadDailyTrainingBars(ctx, codeList, start.AddDate(-2, 0, 0).Format("2006-01-02"), asof, c.Daily.Adjust)
	if err != nil {
		return err
	}
	var marketBars []market.Bar
	if marketContext {
		marketBars, err = loadPredictorMarketBars(ctx, c, asof)
		if err != nil {
			notes = append(notes, "market context unavailable: "+err.Error())
		}
	} else {
		notes = append(notes, "market context disabled for this evaluation")
	}
	regimes, contextOK := loadPredictorRegimes(ctx, c, asof, tradingDays)
	if !contextOK {
		notes = append(notes, "market context unavailable; all market states are unknown")
	}

	lastUniverse := ""
	var lastSamples []predictor.Sample
	provider := func(date string) (predictor.EvaluationDay, error) {
		meta, ok := metas[date]
		if !ok {
			return predictor.EvaluationDay{}, predictor.ErrEvaluationUnavailable
		}
		universe := strings.Join(meta.codes, ",")
		samples := lastSamples
		if universe != lastUniverse || samples == nil {
			data := predictor.Data{
				Stocks:  meta.stocks,
				Minutes: subsetMinuteBars(minutes, meta.codes),
				Daily:   subsetDailyBars(daily, meta.codes),
				Market:  marketBars,
			}
			built, buildErr := predictor.BuildTrainingSamples(data)
			if buildErr != nil {
				return predictor.EvaluationDay{}, buildErr
			}
			samples = built
			lastUniverse = universe
			lastSamples = samples
		}
		return predictor.EvaluationDay{
			Date: date, Samples: samples, Codes: meta.codes,
			ReportRevision: meta.report.Revision, ReportSource: meta.source,
		}, nil
	}
	report, err := predictor.EvaluateRollingWindows(ctx, candidateDates, provider, predictor.EvaluationOptions{
		AsOf: asof, Windows: windows, BaselineOnly: baselineOnly, CalibrationGrid: calibrationGrid, TradingDays: tradingDays, MarketStates: regimes,
		MinTrainingDays: c.Predictor.MinTrainingDays, MaxTrainingDays: maxTrainingDays, HalfLifeDays: halfLife,
		TrainOptions: trainOptions,
		PredictionHook: func(record predictor.PredictionRecord) error {
			if predictionFile == nil {
				return nil
			}
			return json.NewEncoder(predictionFile).Encode(record)
		},
	})
	if err != nil {
		return err
	}
	report.Universe = universe
	report.Notes = append(report.Notes, notes...)
	return json.NewEncoder(os.Stdout).Encode(report)
}

func limitDiagnosticStocks(stocks []config.Stock, limit int) []config.Stock {
	if limit <= 0 || len(stocks) <= limit {
		return stocks
	}
	out := make([]config.Stock, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, stocks[i*len(stocks)/limit])
	}
	return out
}

func emptyEvalProvider(string) (predictor.EvaluationDay, error) {
	return predictor.EvaluationDay{}, predictor.ErrEvaluationUnavailable
}

func subsetMinuteBars(source map[string][]market.MinuteBar, codes []string) map[string][]market.MinuteBar {
	out := make(map[string][]market.MinuteBar, len(codes))
	for _, code := range codes {
		if bars, ok := source[code]; ok {
			out[code] = bars
		}
	}
	return out
}

func subsetDailyBars(source map[string][]market.Bar, codes []string) map[string][]market.Bar {
	out := make(map[string][]market.Bar, len(codes))
	for _, code := range codes {
		if bars, ok := source[code]; ok {
			out[code] = bars
		}
	}
	return out
}

func parsePredictorWindows(value string) ([]predictor.WindowSpec, error) {
	parts := strings.Split(value, ",")
	days := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		day, err := strconv.Atoi(part)
		if err != nil || day < 1 || day > 7300 {
			return nil, fmt.Errorf("invalid predictor-eval window %q", part)
		}
		if !seen[day] {
			seen[day] = true
			days = append(days, day)
		}
	}
	if len(days) == 0 {
		return nil, errors.New("predictor-eval requires at least one window")
	}
	sort.Ints(days)
	out := make([]predictor.WindowSpec, len(days))
	for i, day := range days {
		out[i] = predictor.WindowSpec{Days: day}
	}
	return out, nil
}

func loadPredictorRegimes(ctx context.Context, c config.Config, asof string, tradingDays []string) (map[string]string, bool) {
	db, err := storage.OpenContextView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return map[string]string{}, false
	}
	defer db.Close()
	bars, err := db.LoadRecentBars(ctx, asof, 120)
	if err != nil {
		return map[string]string{}, false
	}
	index := bars["sh000300"]
	if len(index) == 0 {
		return map[string]string{}, false
	}
	marketBars := make([]predictor.MarketBar, 0, len(index))
	for _, bar := range index {
		marketBars = append(marketBars, predictor.MarketBar{Date: bar.Date, Close: bar.Close})
	}
	return predictor.BuildMarketRegimes(tradingDays, marketBars), true
}

func loadPredictorMarketBars(ctx context.Context, c config.Config, asof string) ([]market.Bar, error) {
	if !c.MarketContext.Enabled {
		return nil, errors.New("market context synchronization is disabled")
	}
	db, err := storage.OpenContextView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	bars, err := db.LoadRecentBarsForCode(ctx, predictor.MarketIndexCode, asof, 120)
	if err != nil {
		return nil, err
	}
	out := make([]market.Bar, 0, len(bars))
	for _, bar := range bars {
		out = append(out, market.Bar{Date: bar.Date, Open: bar.Open, Close: bar.Close, High: bar.High, Low: bar.Low, Volume: bar.Volume})
	}
	if len(out) == 0 {
		return nil, errors.New("CSI 300 context has no bars")
	}
	return out, nil
}
