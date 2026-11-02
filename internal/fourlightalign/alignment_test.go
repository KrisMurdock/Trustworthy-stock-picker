package fourlightalign

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func syntheticHistory() market.History {
	closes := make([]float64, 0, 70)
	for len(closes) < 55 {
		closes = append(closes, 10)
	}
	for index := 1; index <= 5; index++ {
		closes = append(closes, 10-float64(index)*0.2)
	}
	for index := 0; index < 10; index++ {
		closes = append(closes, 12+float64(index)*0.05)
	}
	bars := make([]market.Bar, 0, len(closes))
	date, _ := time.Parse("2006-01-02", "2026-01-05")
	for len(bars) < len(closes) {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			index := len(bars)
			volume := 100.0
			if index >= len(closes)-10 {
				volume = 300
			}
			close := closes[index]
			bars = append(bars, market.Bar{Date: date.Format("2006-01-02"), Open: close, Close: close, High: close + .05, Low: close - .05, Volume: volume})
		}
		date = date.AddDate(0, 0, 1)
	}
	return market.History{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date, FetchedAt: time.Now(), Bars: bars}
}

func observationsFrom(history market.History, first, last int) []Observation {
	rows := []Observation{}
	for index := first; index <= last; index++ {
		evidence := strategy.AnalyzeFourLight(history.Bars[:index+1])
		rows = append(rows, Observation{Code: history.Code, Date: history.Bars[index].Date, Trend: evidence.Trend, Energy: evidence.Energy, MidTerm: evidence.MidTerm, ShortTerm: evidence.ShortTerm})
	}
	return rows
}

func targetMetadata() Target {
	return Target{Software: "target", Version: "test", Period: "daily", Adjust: "qfq"}
}

func TestCompareExactSampleAndTransitions(t *testing.T) {
	history := syntheticHistory()
	rows := observationsFrom(history, 59, 69)
	report, err := Compare(rows, targetMetadata(), map[string]market.History{history.Code: history})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusSampleCompared || report.ObservedRows != len(rows) || report.ApproximationRule != strategy.FourLightRule || report.FormulaVerified {
		t.Fatalf("unexpected report header: %+v", report)
	}
	for _, light := range report.Lights {
		if light.Compared != len(rows) || light.Matches != len(rows) || light.Agreement != 1 {
			t.Fatalf("exact light did not align: %+v", light)
		}
	}
	if report.AllRed.Compared != len(rows) || report.AllRed.Matches != len(rows) || report.MismatchCount != 0 {
		t.Fatalf("exact sample reported mismatches: %+v", report.AllRed)
	}
	if report.Transitions[0].Exact == 0 || report.Transitions[0].Missed != 0 || report.Transitions[0].Extra != 0 {
		t.Fatalf("first all-red transition did not align: %+v", report.Transitions[0])
	}
	if report.Quality.MixedRows == 0 || report.Quality.TargetFirstAllRedEvents == 0 {
		t.Fatalf("sample quality was not measured: %+v", report.Quality)
	}
}

func TestCompareMismatchAndUnknownAreExplicit(t *testing.T) {
	history := syntheticHistory()
	rows := observationsFrom(history, 59, 69)
	rows[2].Trend = strategy.FourLightUnknown
	rows[1].ShortTerm = strategy.FourLightGreen
	report, err := Compare(rows, targetMetadata(), map[string]market.History{history.Code: history})
	if err != nil {
		t.Fatal(err)
	}
	if report.Lights[0].ReferenceUnknown != 1 || report.Lights[3].Compared != len(rows) || report.Lights[3].Matches != len(rows)-1 {
		t.Fatalf("unknown or mismatch denominator changed: %+v", report.Lights)
	}
	if report.MismatchCount == 0 || len(report.Mismatches) == 0 {
		t.Fatal("light mismatch was omitted")
	}
	if report.Transitions[0].Extra == 0 || len(report.EventDifferences) == 0 {
		t.Fatalf("transition mismatch was omitted: %+v", report.Transitions[0])
	}
}

func TestCompareReportsTransitionLeadOrLagRows(t *testing.T) {
	history := syntheticHistory()
	rows := []Observation{}
	for index := 59; index <= 69; index++ {
		evidence := strategy.AnalyzeFourLight(history.Bars[:index])
		rows = append(rows, Observation{Code: history.Code, Date: history.Bars[index].Date, Trend: evidence.Trend, Energy: evidence.Energy, MidTerm: evidence.MidTerm, ShortTerm: evidence.ShortTerm})
	}
	report, err := Compare(rows, targetMetadata(), map[string]market.History{history.Code: history})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.EventDifferences) == 0 {
		t.Fatal("lagging target event was not reported")
	}
	found := false
	for _, difference := range report.EventDifferences {
		if difference.Event == EventFirstAllRed && difference.Relation == "shifted" && difference.OffsetRows != 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("transition offset is missing: %+v", report.EventDifferences)
	}
}

func TestCompareRejectsReferenceGapsAndMissingHistory(t *testing.T) {
	history := syntheticHistory()
	rows := observationsFrom(history, 59, 69)
	gapped := append(append([]Observation{}, rows[:2]...), rows[3:]...)
	if _, err := Compare(gapped, targetMetadata(), map[string]market.History{history.Code: history}); err == nil || !strings.Contains(err.Error(), "continuous") {
		t.Fatalf("trading-day gap accepted: %v", err)
	}
	if _, err := Compare(rows, targetMetadata(), map[string]market.History{}); err == nil {
		t.Fatal("missing history accepted")
	}
	if _, err := Compare(rows, Target{Software: "target", Version: "test", Period: "5m", Adjust: "qfq"}, map[string]market.History{history.Code: history}); err == nil {
		t.Fatal("unsupported target period accepted")
	}
}

func TestReadCSVNormalizesLightsAndRejectsBadRows(t *testing.T) {
	reader := strings.NewReader("date,code,trend,energy,mid_term,short_term,note\n2026-05-06,SZ000001,红,green,未知,Red,ok\n")
	rows, err := ReadCSV(context.Background(), reader)
	if err != nil || len(rows) != 1 || rows[0].Code != "sz000001" || rows[0].Trend != "red" || rows[0].Energy != "green" || rows[0].MidTerm != "unknown" || rows[0].ShortTerm != "red" {
		t.Fatalf("CSV normalization failed: %+v %v", rows, err)
	}
	for _, input := range []string{
		"code,date,trend,energy,mid_term\nsz000001,2026-05-06,red,red,red\n",
		"code,date,trend,energy,mid_term,short_term\nsz000001,2026-05-06,red,red,red,blue\n",
		"code,date,trend,energy,mid_term,short_term\nsz000001,2026-05-06,red,red,red,red\nsz000001,2026-05-06,red,red,red,red\n",
	} {
		if _, err := ReadCSV(context.Background(), strings.NewReader(input)); err == nil {
			t.Fatalf("invalid CSV accepted: %s", input)
		}
	}
}
