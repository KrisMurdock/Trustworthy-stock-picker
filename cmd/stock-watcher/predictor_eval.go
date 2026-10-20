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

func runPredictorEval(ctx context.Context, c config.Config, asof, windowsArg string, validationDays, halfLife int, allowLateReports bool) error {
	windows, err := parsePredictorWindows(windowsArg)
	if err != nil {
		return err
	}
	if validationDays < 1 || validationDays > 60 {
		return errors.New("predictor-eval validation-days must be between 1 and 60")
	}
	if halfLife < 1 || halfLife > 365 {
		return errors.New("predictor-eval half-life must be between 1 and 365")
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
	maxWindow := windows[len(windows)-1].Days
	requiredDays := validationDays + maxWindow
	if len(tradingDays) < requiredDays {
		report, err := predictor.EvaluateRollingWindows(ctx, nil, emptyEvalProvider, predictor.EvaluationOptions{
			AsOf: asof, Windows: windows, TradingDays: tradingDays,
			MinTrainingDays: c.Predictor.MinTrainingDays, HalfLifeDays: halfLife,
			TrainOptions: predictor.DefaultTrainOptions(),
		})
		if err != nil {
			return err
		}
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
	if len(metas) == 0 {
		report, err := predictor.EvaluateRollingWindows(ctx, nil, emptyEvalProvider, predictor.EvaluationOptions{
			AsOf: asof, Windows: windows, TradingDays: tradingDays,
			MinTrainingDays: c.Predictor.MinTrainingDays, HalfLifeDays: halfLife,
			TrainOptions: predictor.DefaultTrainOptions(),
		})
		if err != nil {
			return err
		}
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
		AsOf: asof, Windows: windows, TradingDays: tradingDays, MarketStates: regimes,
		MinTrainingDays: c.Predictor.MinTrainingDays, HalfLifeDays: halfLife,
		TrainOptions: predictor.DefaultTrainOptions(),
	})
	if err != nil {
		return err
	}
	report.Notes = append(report.Notes, notes...)
	return json.NewEncoder(os.Stdout).Encode(report)
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
