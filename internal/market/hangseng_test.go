package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTencentHangSengClientParsesAndFiltersHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("param") == "" {
			http.Error(w, "param required", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"hkHSI":{"qfqday":[["2026-10-19","25800.1","25920.2","26000.3","25700.4","1000"],["2026-10-20","25910.2","26010.4","26100.5","25800.6","1200"],["2026-10-21","26020.3","26120.5","26200.6","25900.7","1400"]]}}}`))
	}))
	defer server.Close()
	client := NewTencentHangSengClient(5*time.Second, 0)
	client.Endpoint = server.URL
	bars, err := client.FetchDaily(context.Background(), "2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[1].Date != "2026-10-20" || bars[1].Close != 26010.4 || bars[1].Volume != 1200 {
		t.Fatalf("unexpected hang seng bars: %+v", bars)
	}
}

func TestTencentHangSengRejectsMalformedRows(t *testing.T) {
	if _, err := parseTencentHangSeng([]byte(`{"code":0,"data":{"hkHSI":{"day":[["2026-10-20","1","2","0","1","1"]]}}}`), "2026-10-20"); err == nil {
		t.Fatal("malformed hang seng row accepted")
	}
}
