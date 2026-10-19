package strategy

import (
	"testing"

	"stock-watcher/internal/market"
)

func TestLegacyChanDiagnosticDoesNotChangeRecommendation(t *testing.T) {
	result := Result{Ranking: []Candidate{{Code: "sz000001", Recommended: true, DailyRecommended: true}}}
	minutes, daily := chanMinuteFixture(1)
	date := "2026-09-01"
	bar := daily[date]
	attachLegacyChanEvidence(&result, []market.History{{Code: "sz000001", Bars: []market.Bar{bar}}}, []market.MinuteHistory{minutes}, date)

	candidate := result.Ranking[0]
	if !candidate.Recommended || !candidate.DailyRecommended {
		t.Fatal("legacy Chan diagnostic changed the daily recommendation")
	}
	if candidate.Chan == nil || candidate.Chan.Status != ChanUnknown {
		t.Fatalf("Chan diagnostic not attached: %+v", candidate.Chan)
	}
	if containsString(candidate.Risks, "chan_unknown") || containsString(candidate.Risks, "chan_filtered") {
		t.Fatalf("legacy diagnostic added a filtering risk: %v", candidate.Risks)
	}
}

func TestLegacyChanDiagnosticPassKeepsRecommendation(t *testing.T) {
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
	result := Result{Ranking: []Candidate{{Code: "sz000001", Recommended: true, DailyRecommended: true}}}
	attachLegacyChanEvidence(&result, []market.History{{Code: "sz000001", Bars: []market.Bar{bar}}}, []market.MinuteHistory{minutes}, date)
	candidate := result.Ranking[0]
	if !candidate.Recommended || candidate.Chan == nil || candidate.Chan.Status != ChanPass {
		t.Fatalf("legacy pass changed recommendation: %+v", candidate)
	}
}

func TestLegacyChanDiagnosticSkipsWhenNoMinutes(t *testing.T) {
	result := Result{Ranking: []Candidate{{Code: "sz000001", Recommended: true, DailyRecommended: true}}}
	attachLegacyChanEvidence(&result, nil, nil, "2026-09-01")
	if !result.Ranking[0].Recommended || result.Ranking[0].Chan != nil {
		t.Fatalf("missing minute data should preserve daily-only behavior: %+v", result.Ranking[0])
	}
}

func TestLegacyChanDiagnosticRejectsInvalidMinuteHistoryWithoutChangingRecommendation(t *testing.T) {
	minutes, daily := chanMinuteFixture(1)
	minutes.Source = "invalid"
	date := "2026-09-01"
	result := Result{Ranking: []Candidate{{Code: "sz000001", Recommended: true, DailyRecommended: true}}}
	attachLegacyChanEvidence(&result, []market.History{{Code: "sz000001", Bars: []market.Bar{daily[date]}}}, []market.MinuteHistory{minutes}, date)
	candidate := result.Ranking[0]
	if !candidate.Recommended || candidate.Chan == nil || candidate.Chan.Status != ChanUnknown || candidate.Chan.Reason != "5分钟数据无效" {
		t.Fatalf("invalid minute history was not recorded as unknown: %+v", candidate)
	}
}
