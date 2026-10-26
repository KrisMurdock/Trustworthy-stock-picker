package selection

import (
	"fmt"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestAttachArticleChartsKeepsArticleCandidatesOnly(t *testing.T) {
	report := Report{}
	histories := make([]market.History, 0, 12)
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 12; index++ {
		code := fmt.Sprintf("sz%06d", index+1)
		report.Ranking = append(report.Ranking, strategy.Candidate{Code: code, Rank: index + 1})
		bars := make([]market.Bar, 0, 100)
		for day := 0; day < 100; day++ {
			price := 10 + .02*float64(day) + .1*float64(index%3)
			bars = append(bars, market.Bar{Date: start.AddDate(0, 0, day).Format("2006-01-02"), Open: price - .05, High: price + .2, Low: price - .2, Close: price, Volume: 1000})
		}
		histories = append(histories, market.History{Version: 1, Code: code, Name: code, Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date, Bars: bars})
	}
	report.SecondStage = &secondstage.Report{Candidates: []secondstage.Candidate{{Code: "sz000012", Rank: 1}}}
	attachArticleCharts(&report, histories)
	for index := 0; index < 10; index++ {
		if report.Ranking[index].Chart == nil {
			t.Fatalf("top candidate %d lost chart", index+1)
		}
	}
	if report.Ranking[10].Chart != nil {
		t.Fatal("non-article candidate received a chart")
	}
	if report.Ranking[11].Chart == nil {
		t.Fatal("second-stage candidate outside ranking top 10 lost chart")
	}
}
