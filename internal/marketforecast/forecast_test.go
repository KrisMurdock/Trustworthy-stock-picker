package marketforecast

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func businessDays(start time.Time, count int) []time.Time {
	out := make([]time.Time, 0, count)
	for day := start; len(out) < count; day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		out = append(out, day)
	}
	return out
}

func syntheticSeries(index Index, seed float64, count int) Series {
	days := businessDays(time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC), count)
	bars := make([]Bar, 0, len(days))
	close := 1000 + seed*100
	for i, day := range days {
		wave := math.Sin(float64(i+int(seed))/7) * 0.004
		weekday := 0.0
		switch day.Weekday() {
		case time.Thursday:
			weekday = -0.0015
		case time.Friday:
			weekday = -0.0008
		}
		close *= 1 + 0.0005 + wave*0.15 + weekday
		bars = append(bars, Bar{Date: day.Format("2006-01-02"), Close: close, Open: close, High: close, Low: close})
	}
	return Series{Index: index, Bars: bars}
}

func nextWeekday(date string) (string, error) {
	parsed, err := time.ParseInLocation("2006-01-02", date, time.UTC)
	if err != nil {
		return "", err
	}
	for {
		parsed = parsed.AddDate(0, 0, 1)
		if parsed.Weekday() != time.Saturday && parsed.Weekday() != time.Sunday {
			return parsed.Format("2006-01-02"), nil
		}
	}
}

func TestBuildEmitsDeterministicSignalsAndWeekdayStats(t *testing.T) {
	series := []Series{
		syntheticSeries(DefaultIndices()[0], 1, 260),
		syntheticSeries(DefaultIndices()[1], 2, 260),
	}
	asOf := series[0].Bars[len(series[0].Bars)-1].Date
	options := Options{AsOf: asOf, NextTradingDay: nextWeekday}
	first, err := Build(series, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(series, options)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("market forecast is not deterministic")
	}
	if first.Status != "ok" || len(first.Signals) != 2 || first.DataThrough != asOf {
		t.Fatalf("unexpected report: %+v", first)
	}
	for _, signal := range first.Signals {
		if signal.Status != "ok" || signal.Direction == "" || signal.UpProbability <= 0 || signal.UpProbability >= 1 {
			t.Fatalf("unexpected signal: %+v", signal)
		}
		if signal.TargetDate <= signal.DataThrough || len(signal.Weekdays) != 5 {
			t.Fatalf("signal target or weekdays invalid: %+v", signal)
		}
		if signal.Training.Samples < MinTrainingSamples+MinValidationSample || signal.Validation.Samples != MinValidationSample {
			t.Fatalf("signal sample counts invalid: %+v", signal)
		}
		if signal.Validation.Conclusion == "" {
			t.Fatalf("validation conclusion missing: %+v", signal)
		}
		for _, stat := range signal.Weekdays {
			if stat.Samples != stat.Up+stat.Down+stat.Flat || stat.WilsonLow > stat.WinRate || stat.WilsonHigh < stat.WinRate {
				t.Fatalf("weekday stats invalid: %+v", stat)
			}
		}
	}
}

func TestBuildDoesNotUseBarsAfterAsOf(t *testing.T) {
	series := syntheticSeries(DefaultIndices()[0], 3, 240)
	asOf := series.Bars[190].Date
	options := Options{AsOf: asOf, NextTradingDay: nextWeekday}
	before, err := Build([]Series{series}, options)
	if err != nil {
		t.Fatal(err)
	}
	for i := 191; i < len(series.Bars); i++ {
		series.Bars[i].Close *= 10
	}
	after, err := Build([]Series{series}, options)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("bars after as-of changed the signal")
	}
}

func TestBuildMarksInsufficientAndStaleHistory(t *testing.T) {
	short := syntheticSeries(DefaultIndices()[0], 4, 40)
	report, err := Build([]Series{short}, Options{NextTradingDay: nextWeekday})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "insufficient_history" || report.Signals[0].Status != "insufficient_history" || report.Signals[0].Reason == "" {
		t.Fatalf("short history was accepted: %+v", report)
	}

	long := syntheticSeries(DefaultIndices()[0], 5, 220)
	last := long.Bars[len(long.Bars)-1].Date
	target, _ := nextWeekday(last)
	report, err = Build([]Series{long}, Options{AsOf: target, NextTradingDay: nextWeekday})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "stale" || !report.Signals[0].Stale || report.Signals[0].TargetDate <= last {
		t.Fatalf("stale history was not marked: %+v", report)
	}
}

func TestWeekdayStatsCountReturnsAndWilsonInterval(t *testing.T) {
	dates := []string{
		"2026-01-05", "2026-01-06", "2026-01-07", "2026-01-08", "2026-01-09",
		"2026-01-12", "2026-01-13",
	}
	closes := []float64{100, 101, 102, 97, 98, 99, 100}
	bars := make([]Bar, len(dates))
	for i := range dates {
		bars[i] = Bar{Date: dates[i], Close: closes[i]}
	}
	stats := weekdayStats(bars)
	if len(stats) != 5 {
		t.Fatalf("weekday stats = %+v", stats)
	}
	byDay := map[int]WeekdayStat{}
	total := 0
	for _, stat := range stats {
		byDay[stat.Weekday] = stat
		total += stat.Samples
	}
	if total != len(dates)-1 {
		t.Fatalf("weekday samples = %d, want %d", total, len(dates)-1)
	}
	if byDay[4].Down != 1 || byDay[4].WinRate != 0 || byDay[4].MeanReturnPct >= 0 {
		t.Fatalf("Thursday stats = %+v", byDay[4])
	}
	if byDay[2].Samples != 2 || byDay[2].Up != 2 || byDay[2].WinRate != 1 {
		t.Fatalf("Tuesday stats = %+v", byDay[2])
	}
	if byDay[4].WilsonLow < 0 || byDay[4].WilsonHigh <= byDay[4].WilsonLow || byDay[4].WilsonHigh >= 1 {
		t.Fatalf("Wilson interval = %+v", byDay[4])
	}
}

func TestBuildRejectsDuplicateIndexAndInvalidAsOf(t *testing.T) {
	index := DefaultIndices()[0]
	series := syntheticSeries(index, 6, 200)
	if _, err := Build([]Series{series, series}, Options{NextTradingDay: nextWeekday}); err == nil {
		t.Fatal("duplicate index was accepted")
	}
	if _, err := Build([]Series{series}, Options{AsOf: "2026-2-1", NextTradingDay: nextWeekday}); err == nil {
		t.Fatal("invalid as-of was accepted")
	}
}
