package report

import (
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
)

func TestFirstStageReviewCardShowsRecallAndExceptions(t *testing.T) {
	report := firststage.ReviewReport{
		Version:              firststage.ReviewSchemaVersion,
		Status:               firststage.ReviewStatusOK,
		SignalDate:           "2026-10-21",
		EvaluationDate:       "2026-10-22",
		ThresholdPct:         1,
		SelectionLimit:       30,
		Selected:             30,
		Evaluated:            29,
		Unavailable:          1,
		Hits:                 18,
		Misses:               11,
		TouchHits:            22,
		PoolHits:             40,
		PoolTouchHits:        52,
		SelectionHitRatePct:  62.07,
		RecallPct:            45,
		RecallAvailable:      true,
		TouchHitRatePct:      75.86,
		TouchRecallPct:       42.31,
		TouchRecallAvailable: true,
		Candidates: []firststage.ReviewCandidate{
			{Rank: 1, Code: "sz000001", Name: "命中股", Status: "hit", Correct: true, CloseReturnPct: 2.1},
			{Rank: 2, Code: "sz000002", Name: "未命中股", Status: "miss", CloseReturnPct: -1.2, HighReturnPct: 1.5},
			{Rank: 3, Code: "sz000003", Name: "缺失股", Status: "missing_evaluation"},
		},
	}
	card, err := FirstStageReviewCard(report)
	if err != nil {
		t.Fatal(err)
	}
	if card.EventID != "first-stage-review:2026-10-21:2026-10-22" || !strings.Contains(card.Title, "收盘召回率") {
		t.Fatalf("unexpected card identity: %+v", card)
	}
	if !strings.Contains(card.Sections[0].Lines[2], "45.00%") || !strings.Contains(card.Sections[0].Lines[3], "62.07%") {
		t.Fatalf("unexpected recall lines: %+v", card.Sections[0].Lines)
	}
	if card.Sections[0].Table == nil || len(card.Sections[0].Table.Rows) != 2 {
		t.Fatalf("unexpected exceptions table: %+v", card.Sections[0].Table)
	}
	wantExpiry := time.Date(2026, 10, 25, 23, 59, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !card.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expiry = %s, want %s", card.ExpiresAt, wantExpiry)
	}
}

func TestFirstStageReviewCardMarksUnavailableRecall(t *testing.T) {
	report := firststage.ReviewReport{
		Version: firststage.ReviewSchemaVersion, Status: firststage.ReviewStatusOK,
		SignalDate: "2026-10-21", EvaluationDate: "2026-10-22", ThresholdPct: 1,
		Selected: 2, Evaluated: 2, PoolEvaluated: 2,
	}
	card, err := FirstStageReviewCard(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(card.Sections[0].Lines[2], "不适用") {
		t.Fatalf("missing unavailable recall text: %+v", card.Sections[0].Lines)
	}
}
