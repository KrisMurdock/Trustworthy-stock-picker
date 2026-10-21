package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func TestFirstStageCardPagesStayBelowPayloadLimit(t *testing.T) {
	candidates := make([]firststage.Candidate, 25)
	for i := range candidates {
		candidates[i] = firststage.Candidate{
			Rank:       i + 1,
			Code:       fmt.Sprintf("sz%06d", i+1),
			Name:       strings.Repeat("测试股票", 8),
			BaseScore:  88.75,
			ModelScore: 0.512345,
			BlendScore: 0.75,
		}
	}
	r := selection.Report{
		Version: 3, ID: "report-id", AsOf: "2026-10-21", Target: "2026-10-22",
		Strategy: "short_horizon_v1", SelectionEnabled: true, Adjust: "qfq", PoolSize: 148, Eligible: 148,
		FirstStage: &firststage.Report{
			Version: firststage.ReportSchemaVersion, Status: firststage.StatusOK,
			AsOf: "2026-10-21", Target: "2026-10-22", ModelID: firststage.ModelID,
			ModelVersion: firststage.ModelSchemaVersion, TrainedThrough: "2026-10-15",
			Budget: 145, PoolSize: 148, Eligible: 148, PushEnabled: true, Candidates: candidates,
		},
	}
	cards, err := reportmessage.FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.FirstStage) != 1 {
		t.Fatalf("first-stage pages = %d", len(cards.FirstStage))
	}
	data, err := json.Marshal(Card(cards.FirstStage[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxPayloadBytes {
		t.Fatalf("first-stage card payload = %d bytes, limit %d", len(data), maxPayloadBytes)
	}
}
