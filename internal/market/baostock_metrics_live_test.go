package market

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func TestLiveBaoStockDailyMetrics(t *testing.T) {
	if os.Getenv("LIVE_BAOSTOCK") == "" {
		t.Skip("set LIVE_BAOSTOCK=1 to query the public endpoint")
	}
	client := NewBaoStockClient(15*time.Second, 350*time.Millisecond)
	history, err := client.FetchDailyMetrics(context.Background(), config.Stock{Code: "sh600519"}, "2026-09-01", "2026-10-26")
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(history.Metrics) < 20 || history.Metrics[len(history.Metrics)-1].TurnoverRatePct <= 0 || history.Metrics[len(history.Metrics)-1].AmountCNY <= 0 {
		t.Fatalf("unexpected metrics: %+v", history.Metrics[len(history.Metrics)-1])
	}
	t.Logf("rows=%d first=%+v last=%+v", len(history.Metrics), history.Metrics[0], history.Metrics[len(history.Metrics)-1])
}
