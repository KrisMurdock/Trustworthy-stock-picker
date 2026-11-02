package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func TestSinaFundamentals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sh600519/jsvar.js" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Referer") != "https://finance.sina.com.cn/" {
			t.Errorf("referer = %q", r.Header.Get("Referer"))
		}
		_, _ = w.Write([]byte(`
var totalcapital = 125008.160100; //总股本
var fourQ_mgsy = 65.6615;//最近四个季度每股收益和
var lastyear_mgsy = 65.6600;//前一年每股收益和
var mgjzc = 216.949629;//最近报告的每股净资产
var profit_four = 823.2007;//最近四个季度净利润
var stock_state = 1;
var stockname = '贵州茅台';
var papercode = 'sh600519';
`))
	}))
	defer server.Close()
	client := NewSinaClient(time.Second, 0)
	client.Endpoint = server.URL
	snapshot, err := client.FetchFundamentals(context.Background(), config.Stock{Code: "sh600519"})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if snapshot.EPS != 65.6615 || snapshot.EPSPrior != 65.66 || snapshot.BookValuePerShare != 216.949629 || !snapshot.GrowthKnown {
		t.Fatalf("unexpected fundamentals: %+v", snapshot)
	}
	if snapshot.Shares != 1_250_081_601 || snapshot.TTMNetProfitYi != 823.2007 {
		t.Fatalf("unexpected units: %+v", snapshot)
	}
}

func TestSinaFundamentalsAllowsOptionalGrowthAndBookValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
var totalcapital = 10000;
var fourQ_mgsy = 1;
var profit_four = 1;
var stock_state = 1;
var stockname = '可选字段';
var papercode = 'sh600519';
`))
	}))
	defer server.Close()
	client := NewSinaClient(time.Second, 0)
	client.Endpoint = server.URL
	snapshot, err := client.FetchFundamentals(context.Background(), config.Stock{Code: "sh600519"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EPS != 1 || snapshot.EPSPrior != 0 || snapshot.BookValuePerShare != 0 || snapshot.GrowthKnown {
		t.Fatalf("optional fields were not degraded: %+v", snapshot)
	}
}

func TestSinaFundamentalSnapshotEPSReconciliationTolerance(t *testing.T) {
	base := FundamentalSnapshot{
		Version: 1, Code: "sh600519", Source: "sina", FetchedAt: time.Now().In(Shanghai),
		EPS: 1, BookValuePerShare: 10, Shares: 100_000_000,
		FinancialStatementBasis: "trailing_four_quarters",
	}
	tests := []struct {
		name     string
		ttmYi    float64
		wantErr  bool
		wantNear bool
	}{
		{name: "exact", ttmYi: 1.00},
		{name: "near hard bound", ttmYi: 1.06, wantNear: true},
		{name: "outside hard bound", ttmYi: 1.11, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			snapshot.TTMNetProfitYi = test.ttmYi
			err := snapshot.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v: %+v", err, test.wantErr, snapshot)
			}
			if got := snapshot.EPSReconciliationNearTolerance(); got != test.wantNear {
				t.Fatalf("EPSReconciliationNearTolerance() = %v, want %v: %+v", got, test.wantNear, snapshot)
			}
		})
	}
}

func TestSinaFundamentalsRejectMismatchedStock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
var totalcapital = 1;
var fourQ_mgsy = 1;
var lastyear_mgsy = 1;
var mgjzc = 1;
var profit_four = 1;
var stock_state = 1;
var stockname = 'wrong';
var papercode = 'sz000001';
`))
	}))
	defer server.Close()
	client := NewSinaClient(time.Second, 0)
	client.Endpoint = server.URL
	_, err := client.FetchFundamentals(context.Background(), config.Stock{Code: "sh600519"})
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("mismatched stock accepted: %v", err)
	}
}

func TestSinaFundamentalsRejectDelistedState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
var totalcapital = 1;
var fourQ_mgsy = 1;
var lastyear_mgsy = 1;
var mgjzc = 1;
var profit_four = 1;
var stock_state = 3;
var stockname = 'delisted';
var papercode = 'sh600519';
`))
	}))
	defer server.Close()
	client := NewSinaClient(time.Second, 0)
	client.Endpoint = server.URL
	_, err := client.FetchFundamentals(context.Background(), config.Stock{Code: "sh600519"})
	if err == nil || !strings.Contains(err.Error(), "not_normally_listed") {
		t.Fatalf("delisted stock accepted: %v", err)
	}
}

func TestLiveSinaFundamentals(t *testing.T) {
	if os.Getenv("LIVE_SINA") == "" {
		t.Skip("set LIVE_SINA=1 to query the public endpoint")
	}
	client := NewSinaClient(15*time.Second, 350*time.Millisecond)
	snapshot, err := client.FetchFundamentals(context.Background(), config.Stock{Code: "sh600519"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EPS <= 0 || snapshot.BookValuePerShare <= 0 || snapshot.Shares <= 0 {
		t.Fatalf("unexpected live fundamentals: %+v", snapshot)
	}
	t.Logf("eps=%.4f bps=%.4f prior_eps=%.4f shares=%.0f", snapshot.EPS, snapshot.BookValuePerShare, snapshot.EPSPrior, snapshot.Shares)
}
