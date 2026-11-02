package strategy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func fourLightBars(count int, lastClose, lastVolume float64) []market.Bar {
	bars := make([]market.Bar, 0, count)
	date, _ := time.Parse("2006-01-02", "2026-01-05")
	for len(bars) < count {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			close := 10 + float64(len(bars))*0.01
			volume := 100.0
			if len(bars) == count-1 {
				close, volume = lastClose, lastVolume
			}
			bars = append(bars, market.Bar{Date: date.Format("2006-01-02"), Open: close, Close: close, High: close + .05, Low: close - .05, Volume: volume})
		}
		date = date.AddDate(0, 0, 1)
	}
	return bars
}

func TestFourLightRequiresFinalDataAndDoesNotTreatUnknownAsRed(t *testing.T) {
	ready := AnalyzeFourLight(fourLightBars(60, 12, 200))
	if !ready.AllRed || ready.FinalOnly != true || !ready.Approximation || ready.AlignmentStatus != FourLightAlignmentUnverified || ready.DataStatus != FourLightDataPartial || ready.PreviousUnknown != 4 {
		t.Fatalf("unexpected final-bar state: %+v", ready)
	}
	if ready.FlipCount != 0 || ready.AllFlipped || matchesFourLightRule(FourLightRuleFlipAny, ready) {
		t.Fatalf("missing previous day produced a flip: %+v", ready)
	}

	short := AnalyzeFourLight(fourLightBars(59, 12, 200))
	if short.AllRed || short.UnknownLights != 4 || short.DataStatus != FourLightDataUnknown || matchesFourLightRule(FourLightRuleAllRed, short) {
		t.Fatalf("insufficient history passed resonance: %+v", short)
	}

	suspended := fourLightBars(61, 12, 200)
	suspended[len(suspended)-1].Volume = 0
	unknown := AnalyzeFourLight(suspended)
	if unknown.Energy != FourLightUnknown || unknown.AllRed || unknown.DataStatus != FourLightDataUnknown || matchesFourLightRule(FourLightRuleAllRed, unknown) {
		t.Fatalf("zero-volume signal passed resonance: %+v", unknown)
	}
}

func TestFourLightTransitionsAndRulesStayConsistent(t *testing.T) {
	bars := make([]market.Bar, 0, 61)
	date, _ := time.Parse("2006-01-02", "2026-01-05")
	closes := make([]float64, 0, 61)
	for len(closes) < 55 {
		closes = append(closes, 10)
	}
	for i := 0; i < 5; i++ {
		closes = append(closes, 10-float64(i+1)*0.2)
	}
	closes = append(closes, 12)
	for len(bars) < len(closes) {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			i := len(bars)
			volume := 100.0
			if i == len(closes)-1 {
				volume = 300
			}
			bars = append(bars, market.Bar{Date: date.Format("2006-01-02"), Open: closes[i], Close: closes[i], High: closes[i] + .05, Low: closes[i] - .05, Volume: volume})
		}
		date = date.AddDate(0, 0, 1)
	}
	evidence := AnalyzeFourLight(bars)
	if evidence.DataStatus != FourLightDataReady || !evidence.AllRed || evidence.FlipCount != 4 || !evidence.AllFlipped {
		t.Fatalf("four-light flip mismatch: %+v", evidence)
	}
	if !matchesFourLightRule(FourLightRuleAllRed, evidence) || !matchesFourLightRule(FourLightRuleFlipAny, evidence) || !matchesFourLightRule(FourLightRuleFlipAll, evidence) {
		t.Fatalf("all-red and flip rules disagree: %+v", evidence)
	}
	flips := 0
	for _, flipped := range []bool{evidence.Transitions.Trend, evidence.Transitions.Energy, evidence.Transitions.MidTerm, evidence.Transitions.ShortTerm} {
		if flipped {
			flips++
		}
	}
	if flips != evidence.FlipCount || !validFourLightRule(FourLightRuleAllRed) || validFourLightRule("typo") {
		t.Fatalf("transition count or rule vocabulary invalid: %+v", evidence)
	}
}

func TestFourLightFutureBarCannotChangePastEvidence(t *testing.T) {
	bars := fourLightBars(61, 12, 200)
	before := AnalyzeFourLight(bars[:60])
	bars = append(bars, market.Bar{Date: "2027-01-04", Open: 99, Close: 99, High: 100, Low: 98, Volume: 9999})
	after := AnalyzeFourLight(bars[:60])
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("future bar changed a completed signal")
	}
}

func TestCandidateFourLightFieldKeepsOldReportsReadable(t *testing.T) {
	var old Candidate
	if err := json.Unmarshal([]byte(`{"code":"sz000001","rank":1,"opportunity_score":50}`), &old); err != nil || old.FourLight != nil {
		t.Fatalf("old report failed to decode: %+v %v", old, err)
	}
	evidence := AnalyzeFourLight(fourLightBars(61, 12, 200))
	encoded, err := json.Marshal(Candidate{Code: "sz000001", FourLight: &evidence})
	if err != nil {
		t.Fatal(err)
	}
	var decoded Candidate
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.FourLight == nil || decoded.FourLight.Rule != FourLightRule {
		t.Fatalf("four-light evidence did not round-trip: %s %v", encoded, err)
	}
}

func TestFourLightPoliciesFilterEntriesWithoutChangingBaseline(t *testing.T) {
	c, histories, raw := researchFixture()
	for i := range histories[0].Bars {
		histories[0].Bars[i].Volume = 100
		if i%2 == 1 {
			histories[0].Bars[i].Volume = 300
		}
	}
	ctx := context.Background()
	baseline, err := AnalyzeDevelopment(ctx, c, histories, raw, histories[0].AsOf, EntryPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	allRed, err := AnalyzeDevelopment(ctx, c, histories, raw, histories[0].AsOf, EntryPolicy{FourLightRule: FourLightRuleAllRed})
	if err != nil {
		t.Fatal(err)
	}
	flipAny, err := AnalyzeDevelopment(ctx, c, histories, raw, histories[0].AsOf, EntryPolicy{FourLightRule: FourLightRuleFlipAny})
	if err != nil {
		t.Fatal(err)
	}
	flipAll, err := AnalyzeDevelopment(ctx, c, histories, raw, histories[0].AsOf, EntryPolicy{FourLightRule: FourLightRuleFlipAll})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(baseline.Research.Development.PoolBaseline)
	for name, result := range map[string]DevelopmentResult{
		"baseline": baseline,
		"all-red":  allRed,
		"flip-any": flipAny,
		"flip-all": flipAll,
	} {
		if summary := result.Research.FourLight; summary == nil || summary.SignalDates != result.Research.Development.SignalDates {
			t.Fatalf("%s four-light summary escaped the development interval: %+v", name, result.Research.FourLight)
		}
	}
	for _, result := range []DevelopmentResult{allRed, flipAny, flipAll} {
		b, _ := json.Marshal(result.Research.Development.PoolBaseline)
		if string(a) != string(b) {
			t.Fatal("four-light filter contaminated the pool baseline")
		}
	}
	if allRed.Research.Development.TopRanked.Samples == 0 || flipAny.Research.Development.TopRanked.Samples == 0 || flipAll.Research.Development.TopRanked.Samples != 0 {
		t.Fatalf("unexpected rule selection counts: all=%d any=%d all-flip=%d", allRed.Research.Development.TopRanked.Samples, flipAny.Research.Development.TopRanked.Samples, flipAll.Research.Development.TopRanked.Samples)
	}
	for _, trade := range append(allRed.Trades, flipAny.Trades...) {
		if trade.Selected && (trade.FourLight == nil || !trade.FourLight.AllRed || trade.FourLight.UnknownLights > 0 || trade.FourLight.FlipCount == 0) {
			t.Fatalf("unknown or non-resonant trade was selected: %+v", trade)
		}
	}
	if _, err := AnalyzeDevelopment(ctx, c, histories, raw, histories[0].AsOf, EntryPolicy{FourLightRule: "typo"}); err == nil {
		t.Fatal("unknown four-light rule accepted")
	}
}
