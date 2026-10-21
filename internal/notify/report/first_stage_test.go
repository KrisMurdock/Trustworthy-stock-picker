package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func TestFirstStageCardsShowTop30InOneCardAndCarryDisclaimer(t *testing.T) {
	candidates := make([]firststage.Candidate, 145)
	for i := range candidates {
		candidates[i] = firststage.Candidate{
			Rank:       i + 1,
			Code:       fmt.Sprintf("sz%06d", i+1),
			Name:       fmt.Sprintf("测试%d", i+1),
			BaseScore:  float64(80-i%10) + 0.25,
			ModelScore: 0.5 + float64(i)/1000,
			BlendScore: 1 - float64(i)/100,
		}
	}
	r := selection.Report{
		Version:          3,
		ID:               "report-id",
		AsOf:             "2026-10-21",
		Target:           "2026-10-22",
		Strategy:         "short_horizon_v1",
		SelectionEnabled: true,
		Adjust:           "qfq",
		PoolSize:         148,
		Eligible:         148,
		FirstStage: &firststage.Report{
			Version:        firststage.ReportSchemaVersion,
			Status:         firststage.StatusOK,
			AsOf:           "2026-10-21",
			Target:         "2026-10-22",
			ModelID:        firststage.ModelID,
			ModelVersion:   firststage.ModelSchemaVersion,
			TrainedThrough: "2026-10-15",
			Budget:         145,
			PoolSize:       148,
			Eligible:       148,
			PushEnabled:    true,
			Candidates:     candidates,
		},
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.FirstStage) != 1 {
		t.Fatalf("first-stage pages = %d, want 1", len(cards.FirstStage))
	}
	first := cards.FirstStage[0]
	if !strings.Contains(first.Title, "30 只") || strings.Contains(first.Title, "1/1") {
		t.Fatalf("unexpected first title: %s", first.Title)
	}
	if !strings.Contains(first.Sections[0].Lines[0], "不构成交易建议") || !strings.Contains(first.Sections[0].Lines[2], "0.5") {
		t.Fatalf("missing disclaimer or formula: %+v", first.Sections[0].Lines)
	}
	table := first.Sections[0].Table
	if table == nil || len(table.Rows) != 30 {
		t.Fatalf("first-stage rows = %+v", table)
	}
	if table.Rows[0][0].Text != "1" || table.Rows[29][0].Text != "30" {
		t.Fatalf("unexpected first-stage rank range: first=%s last=%s", table.Rows[0][0].Text, table.Rows[29][0].Text)
	}
	seen := map[string]bool{}
	expires := time.Date(2026, 10, 22, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	for _, card := range cards.FirstStage {
		if seen[card.EventID] || card.EventID == cards.Main.EventID || !card.ExpiresAt.Equal(expires) {
			t.Fatalf("invalid first-stage card identity: %+v", card)
		}
		seen[card.EventID] = true
	}
}

func TestFirstStageCardsRequireExplicitPush(t *testing.T) {
	r := selection.Report{
		Version: 1, ID: "report-id", AsOf: "2026-10-21", Target: "2026-10-22",
		SelectionEnabled: true, Adjust: "qfq", PoolSize: 1,
		FirstStage: &firststage.Report{
			Version: firststage.ReportSchemaVersion, Status: firststage.StatusOK,
			AsOf: "2026-10-21", Target: "2026-10-22", PushEnabled: false,
			Candidates: []firststage.Candidate{{Rank: 1, Code: "sz000001", Name: "测试"}},
		},
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.FirstStage) != 0 {
		t.Fatalf("unexpected first-stage cards: %+v", cards.FirstStage)
	}
}
