package selection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestAttachTrappedSignalsUsesMatchingAdjustedHistory(t *testing.T) {
	report := Report{
		Adjust:  "qfq",
		Ranking: []strategy.Candidate{{Code: "sz000001"}, {Code: "sz000002"}},
	}
	bars := make([]market.Bar, 120)
	for index := range bars {
		price := 10.0
		if index < 80 {
			price = 12
		}
		bars[index] = market.Bar{Open: price, High: price, Low: price, Close: price, Volume: 100}
	}
	histories := []market.History{
		{Code: "sz000001", Adjust: "qfq", Bars: bars},
		{Code: "sz000002", Adjust: "none", Bars: bars},
	}

	attachTrappedSignals(&report, histories)

	if report.Ranking[0].Trapped == nil || report.Ranking[0].Trapped.Status != strategy.TrappedStatusTriggered {
		t.Fatalf("triggered signal not attached: %+v", report.Ranking[0].Trapped)
	}
	if report.Ranking[1].Trapped != nil {
		t.Fatalf("mismatched adjustment was used: %+v", report.Ranking[1].Trapped)
	}
	encoded, err := json.Marshal(report.Ranking[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"trapped_signal"`) {
		t.Fatalf("trapped signal is not persisted in candidate JSON: %s", encoded)
	}
}
