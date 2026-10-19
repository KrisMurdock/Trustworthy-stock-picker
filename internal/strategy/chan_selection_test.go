package strategy

import (
	"testing"

	"stock-watcher/internal/market"
)

func TestApplyChanGateUnknownDisablesRecommendation(t *testing.T) {
	result := Result{Ranking: []Candidate{{Code: "sz000001", Recommended: true}}}
	minutes, daily := chanMinuteFixture(1)
	date := "2026-09-01"
	bar := daily[date]
	applyChanGate(&result, []market.History{{Code: "sz000001", Bars: []market.Bar{bar}}}, []market.MinuteHistory{minutes}, date)

	candidate := result.Ranking[0]
	if candidate.Recommended {
		t.Fatal("unknown Chan evidence still allowed a recommendation")
	}
	if candidate.Chan == nil || candidate.Chan.Status != ChanUnknown {
		t.Fatalf("Chan evidence not attached: %+v", candidate.Chan)
	}
	if !containsString(candidate.Risks, "chan_unknown") {
		t.Fatalf("unknown Chan risk missing: %v", candidate.Risks)
	}
}

func TestApplyChanGatePassKeepsRecommendation(t *testing.T) {
	minutes, daily := chanMinuteFixture(13)
	var date string
	var bar market.Bar
	for day, candidateBar := range daily {
		evidence, err := chanSignalEvidence(minutes, day, candidateBar)
		if err == nil && evidence.Status == ChanPass {
			date, bar = day, candidateBar
			break
		}
	}
	if date == "" {
		t.Fatal("minute fixture produced no Chan pass day")
	}
	result := Result{Ranking: []Candidate{{Code: "sz000001", Recommended: true}}}
	applyChanGate(&result, []market.History{{Code: "sz000001", Bars: []market.Bar{bar}}}, []market.MinuteHistory{minutes}, date)
	candidate := result.Ranking[0]
	if !candidate.Recommended || candidate.Chan == nil || candidate.Chan.Status != ChanPass {
		t.Fatalf("passing Chan evidence disabled recommendation: %+v", candidate)
	}
}

func TestApplyChanGateSkipsWhenNoMinutes(t *testing.T) {
	result := Result{Ranking: []Candidate{{Code: "sz000001", Recommended: true}}}
	applyChanGate(&result, nil, nil, "2026-09-01")
	if !result.Ranking[0].Recommended || result.Ranking[0].Chan != nil {
		t.Fatalf("missing minute data should preserve daily-only behavior: %+v", result.Ranking[0])
	}
}

func TestApplyChanGateRejectsInvalidMinuteHistory(t *testing.T) {
	minutes, daily := chanMinuteFixture(1)
	minutes.Source = "invalid"
	date := "2026-09-01"
	result := Result{Ranking: []Candidate{{Code: "sz000001", Recommended: true}}}
	applyChanGate(&result, []market.History{{Code: "sz000001", Bars: []market.Bar{daily[date]}}}, []market.MinuteHistory{minutes}, date)
	candidate := result.Ranking[0]
	if candidate.Recommended || candidate.Chan == nil || candidate.Chan.Status != ChanUnknown || candidate.Chan.Reason != "5分钟数据无效" {
		t.Fatalf("invalid minute history was not rejected as unknown: %+v", candidate)
	}
}
