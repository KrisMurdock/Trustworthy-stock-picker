package strategy

import (
	"math"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

const guardTestAsOf = "2026-10-16"

func guardWeekdays(end string, count int) []string {
	last, _ := time.ParseInLocation("2006-01-02", end, market.Shanghai)
	days := []string{}
	for day := last; len(days) < count; day = day.AddDate(0, 0, -1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			days = append(days, day.Format("2006-01-02"))
		}
	}
	for i, j := 0, len(days)-1; i < j; i, j = i+1, j-1 {
		days[i], days[j] = days[j], days[i]
	}
	return days
}

func guardDailyHistory(code string, count int, amplitude float64, adjust string) market.History {
	days := guardWeekdays(guardTestAsOf, count)
	h := market.History{Version: 1, Code: code, Name: "结构测试", Source: "tencent", Adjust: adjust, DataField: "day", VolumeUnit: "provider_raw", AsOf: guardTestAsOf, FetchedAt: time.Date(2026, 10, 16, 15, 10, 0, 0, market.Shanghai)}
	if adjust == "qfq" {
		h.DataField = "qfqday"
	}
	for i, day := range days {
		price := 10 + amplitude*math.Sin(2*math.Pi*float64(i)/20)
		h.Bars = append(h.Bars, market.Bar{Date: day, Open: price - 0.05, Close: price, High: price + 0.2, Low: price - 0.2, Volume: 1000})
	}
	return h
}

func guardMinuteHistories(code string, amplitude float64) (market.MinuteHistory, market.MinuteHistory) {
	days := guardWeekdays(guardTestAsOf, 13)
	fetched := time.Date(2026, 10, 16, 15, 10, 0, 0, market.Shanghai)
	base := market.MinuteHistory{Version: 1, Code: code, Name: "结构测试", Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: fetched}
	index := 0
	for _, day := range days {
		date, _ := time.ParseInLocation("2006-01-02", day, market.Shanghai)
		for i := 0; i < 48; i++ {
			minute := 575 + i*5
			if i >= 24 {
				minute = 785 + (i-24)*5
			}
			price := 10 + amplitude*math.Sin(2*math.Pi*float64(index)/20)
			base.Bars = append(base.Bars, market.MinuteBar{End: date.Add(time.Duration(minute) * time.Minute), Open: price - 0.05, High: price + 0.2, Low: price - 0.2, Close: price, Volume: 100, Final: true})
			index++
		}
	}
	context := base
	context.Period = 30
	context.Bars = market.AggregateMinutes(base.Bars, 30, fetched)
	return base, context
}

func TestStructureGuardPassesOnlyWithDailyStructureAndNoLowerVeto(t *testing.T) {
	adjusted := guardDailyHistory("sz000001", 260, 1, "qfq")
	raw := guardDailyHistory("sz000001", 260, 1, "none")
	base, context := guardMinuteHistories("sz000001", 1)
	evidence := evaluateStructureGuard(StructureGuardEnforce, "sz000001", guardTestAsOf, adjusted, raw, []market.MinuteHistory{context, base})
	if evidence.DataStatus != guardDataOK || evidence.GuardStatus != guardPass {
		t.Fatalf("guard = %+v", evidence)
	}
	if evidence.DailyZone == nil || evidence.DailyZone.Lower > 10.5 {
		t.Fatalf("daily zone not captured: %+v", evidence.DailyZone)
	}
	if len(evidence.Levels) != 3 || evidence.Levels[0].Period != "day" || evidence.Levels[1].Period != "30m" || evidence.Levels[2].Period != "5m" {
		t.Fatalf("levels = %+v", evidence.Levels)
	}
}

func TestStructureGuardDoesNotPassWithoutFinishedDailyStroke(t *testing.T) {
	flat := guardDailyHistory("sz000001", 260, 0, "qfq")
	raw := guardDailyHistory("sz000001", 260, 0, "none")
	evidence := evaluateStructureGuard(StructureGuardEnforce, "sz000001", guardTestAsOf, flat, raw, nil)
	if evidence.GuardStatus == guardPass {
		t.Fatalf("flat daily data passed: %+v", evidence)
	}
	if evidence.ReasonCode != "daily_no_structure" {
		t.Fatalf("reason = %s %s", evidence.ReasonCode, evidence.Reason)
	}
}

func TestStructureGuardVetoesThirtyMinuteBelowDailyZone(t *testing.T) {
	adjusted := guardDailyHistory("sz000001", 260, 1, "qfq")
	raw := guardDailyHistory("sz000001", 260, 1, "none")
	base, context := guardMinuteHistories("sz000001", 1)
	for i := range base.Bars {
		base.Bars[i].Open *= 0.5
		base.Bars[i].High *= 0.5
		base.Bars[i].Low *= 0.5
		base.Bars[i].Close *= 0.5
	}
	context.Bars = market.AggregateMinutes(base.Bars, 30, base.FetchedAt)
	evidence := evaluateStructureGuard(StructureGuardEnforce, "sz000001", guardTestAsOf, adjusted, raw, []market.MinuteHistory{context, base})
	if evidence.GuardStatus != guardFail || evidence.ReasonCode != "veto_30m_below_daily_zone" {
		t.Fatalf("30m veto missing: %+v", evidence)
	}
}

func TestStructureGuardInsufficientMinutesStayUnknown(t *testing.T) {
	adjusted := guardDailyHistory("sz000001", 260, 1, "qfq")
	raw := guardDailyHistory("sz000001", 260, 1, "none")
	base, context := guardMinuteHistories("sz000001", 1)
	base.Bars = base.Bars[len(base.Bars)-48:]
	base.FetchedAt = base.Bars[len(base.Bars)-1].End.Add(10 * time.Minute)
	context.Bars = market.AggregateMinutes(base.Bars, 30, base.FetchedAt)
	evidence := evaluateStructureGuard(StructureGuardEnforce, "sz000001", guardTestAsOf, adjusted, raw, []market.MinuteHistory{context, base})
	if evidence.GuardStatus != guardUnknown || evidence.DataStatus != guardDataInsufficient {
		t.Fatalf("insufficient 5m did not stay unknown: %+v", evidence)
	}
}

func TestStructureGuardRejectsMismatchedMinuteSnapshot(t *testing.T) {
	adjusted := guardDailyHistory("sz000001", 260, 1, "qfq")
	raw := guardDailyHistory("sz000001", 260, 1, "none")
	base, context := guardMinuteHistories("sz000001", 1)
	context.FetchedAt = context.FetchedAt.Add(time.Minute)
	evidence := evaluateStructureGuard(StructureGuardEnforce, "sz000001", guardTestAsOf, adjusted, raw, []market.MinuteHistory{context, base})
	if evidence.GuardStatus != guardUnknown || evidence.ReasonCode != "minute_snapshot_invalid" {
		t.Fatalf("mismatched snapshot accepted: %+v", evidence)
	}
}

func TestStructureGuardRanksOnlyPassingPoolAndKeepsObservationView(t *testing.T) {
	codes := []string{"sz000001", "sz000002", "sz000003"}
	histories := []market.History{}
	raw := []market.History{}
	minutes := []market.MinuteHistory{}
	for _, code := range codes {
		histories = append(histories, guardDailyHistory(code, 260, 1, "qfq"))
		raw = append(raw, guardDailyHistory(code, 260, 1, "none"))
	}
	baseA, contextA := guardMinuteHistories(codes[0], 1)
	baseC, contextC := guardMinuteHistories(codes[2], 1)
	minutes = append(minutes, contextA, baseA, contextC, baseC)
	result := Result{Ranking: []Candidate{
		{Code: codes[0], Name: "A", Score: 90, Features: Features{Return5: 1}, Recommended: true, DailyRecommended: true, Risks: []string{"holdout_validation_not_passed"}},
		{Code: codes[1], Name: "B", Score: 80, Features: Features{Return5: 9}, Recommended: true, DailyRecommended: true},
		{Code: codes[2], Name: "C", Score: 70, Features: Features{Return5: 3}, Recommended: true, DailyRecommended: true},
	}}
	c := config.Defaults()
	c.StructureGuard.Mode = StructureGuardEnforce
	applyStructureGuard(&result, c, histories, raw, minutes, guardTestAsOf)
	if result.GuardPassed != 2 {
		t.Fatalf("guard passed = %d", result.GuardPassed)
	}
	if result.Ranking[2].Code != codes[1] || result.Ranking[2].StructureGuard.GuardStatus != guardUnknown {
		t.Fatalf("filtered stock was not moved out of eligible ranking: %+v", result.Ranking)
	}
	if result.Ranking[0].EligibleRank != 1 || result.Ranking[1].EligibleRank != 2 {
		t.Fatalf("eligible ranks missing: %+v", result.Ranking)
	}
	for _, candidate := range result.Ranking[:2] {
		switch candidate.Code {
		case codes[0]:
			if math.Abs(candidate.Features.Relative5-(-1)) > 1e-9 {
				t.Fatalf("A relative5 = %v, want -1", candidate.Features.Relative5)
			}
		case codes[2]:
			if math.Abs(candidate.Features.Relative5-1) > 1e-9 {
				t.Fatalf("C relative5 = %v, want 1", candidate.Features.Relative5)
			}
		}
	}
	if result.Ranking[0].ObservationRank == 0 || result.Ranking[0].ObservationScore == 0 || result.Ranking[0].EligibleScore == 0 {
		t.Fatalf("observation and eligible scores must both be preserved: %+v", result.Ranking[0])
	}
	for _, candidate := range result.Ranking[:2] {
		if candidate.Code == codes[0] && (candidate.EligibleRecommended || !containsString(candidate.Risks, "holdout_validation_not_passed")) {
			t.Fatalf("eligible rerank bypassed the holdout gate: %+v", candidate)
		}
		if candidate.Code == codes[2] && (!candidate.EligibleRecommended || !candidate.Recommended) {
			t.Fatalf("guard-passing eligible candidate was not executable: %+v", candidate)
		}
	}
	if result.Ranking[2].Recommended {
		t.Fatalf("guard-filtered candidate remained executable: %+v", result.Ranking[2])
	}

	shadow := Result{Ranking: []Candidate{
		{Code: codes[0], Name: "A", Score: 90, Features: Features{Return5: 1}, Recommended: true, DailyRecommended: true},
		{Code: codes[1], Name: "B", Score: 80, Features: Features{Return5: 9}, Recommended: true, DailyRecommended: true},
	}}
	c.StructureGuard.Mode = StructureGuardShadow
	applyStructureGuard(&shadow, c, histories, raw, minutes, guardTestAsOf)
	if shadow.ExecutionMode != StructureGuardShadow || shadow.Ranking[0].Recommended || shadow.Ranking[1].Recommended {
		t.Fatalf("shadow mode produced an executable recommendation: %+v", shadow)
	}
	if !shadow.Ranking[0].DailyRecommended || !shadow.Ranking[1].DailyRecommended {
		t.Fatalf("shadow mode lost the daily recommendation audit: %+v", shadow.Ranking)
	}
	if shadow.Ranking[0].EligibleRank != 1 || shadow.Ranking[0].EligibleScore == 0 || shadow.Ranking[1].EligibleRank != 0 {
		t.Fatalf("shadow mode did not retain the eligible view: %+v", shadow.Ranking)
	}
}
