package marketforecast

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildSessionSignalsUsesOnlyPriorKoreanBars(t *testing.T) {
	bars := sessionTestBars(150)
	minutes := sessionTestThirtyMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	korea := []KoreaSeries{
		{Code: "KOSPI", Bars: bars},
		{Code: "KOSDAQ", Bars: bars},
	}
	series := []Series{{
		Index: DefaultIndices()[0], Bars: bars, SessionMinutes: minutes,
	}}
	options := Options{AsOf: bars[len(bars)-1].Date, NextTradingDay: nextWeekday, Korea: korea}
	first, err := Build(series, options)
	if err != nil {
		t.Fatal(err)
	}
	if !first.SessionModel.KoreaFeatures || !containsString(first.SessionModel.FeatureNames, "kospi_ret1") {
		t.Fatalf("korea feature metadata missing: %+v", first.SessionModel)
	}
	if len(first.Signals) != 1 || len(first.Signals[0].Sessions) != 2 {
		t.Fatalf("unexpected signals: %+v", first.Signals)
	}
	for _, signal := range first.Signals[0].Sessions {
		if signal.Status != "ok" || signal.TargetDate != target {
			t.Fatalf("unexpected session signal: %+v", signal)
		}
	}
	before, _ := json.Marshal(first.Signals[0].Sessions)

	// A Korean bar on the target date must not enter a pre-open feature. The
	// latest usable bar remains the one strictly before the target date.
	targetBar := Bar{Date: target, Open: 999, High: 1000, Low: 1, Close: 999}
	korea[0].Bars = append(append([]Bar(nil), bars...), targetBar)
	korea[1].Bars = append(append([]Bar(nil), bars...), targetBar)
	second, err := Build(series, Options{AsOf: bars[len(bars)-1].Date, NextTradingDay: nextWeekday, Korea: korea})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(second.Signals[0].Sessions)
	if string(before) != string(after) {
		t.Fatal("target-day Korean bars changed the pre-open signal")
	}
}

func TestBuildSessionSignalsWithoutCompleteKoreaFallsBack(t *testing.T) {
	bars := sessionTestBars(150)
	minutes := sessionTestThirtyMinutes(bars)
	series := []Series{{Index: DefaultIndices()[0], Bars: bars, SessionMinutes: minutes}}
	report, err := Build(series, Options{
		AsOf: bars[len(bars)-1].Date, NextTradingDay: nextWeekday,
		Korea: []KoreaSeries{{Code: "KOSPI", Bars: bars}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionModel.KoreaFeatures || containsString(report.SessionModel.FeatureNames, "kospi_ret1") {
		t.Fatalf("incomplete korea context enabled korea features: %+v", report.SessionModel)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
