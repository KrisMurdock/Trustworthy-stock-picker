package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func TestMarketContextAPIReadsLatestAndSelectedDate(t *testing.T) {
	root := t.TempDir()
	db, err := storage.InitContext(root, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 19, 0, 0, 0, time.UTC)
	items := []storage.ContextInstrument{
		{Code: "sh000001", Name: "上证指数", Kind: "index", Category: "index", Source: "tencent", UpdatedAt: at},
		{Code: "BK1036", Name: "半导体", Kind: "board", Category: "industry,concept", Source: "eastmoney", UpdatedAt: at},
	}
	for _, item := range items {
		if err := db.SaveInstrument(ctx, item, []storage.ContextBar{
			{Code: item.Code, Date: "2026-10-16", Open: 100, Close: 101, High: 102, Low: 99, Volume: 100},
			{Code: item.Code, Date: "2026-10-19", Open: 101, Close: 103, High: 104, Low: 100, Volume: 120},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveSyncStatus(ctx, storage.ContextSyncStatus{AsOf: "2026-10-19", StartedAt: at.Add(-time.Minute), FinishedAt: at, Status: "complete", Total: 2, Succeeded: 2}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	c := config.Defaults()
	c.DataDir = root
	c.RequireCIFS = false
	h := newHandler(c, &testReader{}, nil)
	w := request(t, h, http.MethodGet, "/api/market-context")
	if w.Code != http.StatusOK {
		t.Fatalf("market context: %d %s", w.Code, w.Body)
	}
	var snapshot struct {
		Version int    `json:"schema_version"`
		AsOf    string `json:"as_of"`
		Indices []struct {
			Code string
		}
		Boards []struct {
			Code      string
			ChangePct float64 `json:"change_pct"`
		}
		Summary struct {
			Industry int `json:"industry"`
			Concept  int `json:"concept"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 || snapshot.AsOf != "2026-10-19" || len(snapshot.Indices) != 1 || len(snapshot.Boards) != 1 || snapshot.Boards[0].Code != "BK1036" || snapshot.Summary.Industry != 1 || snapshot.Summary.Concept != 1 {
		t.Fatalf("unexpected snapshot: %s", w.Body)
	}
	if snapshot.Boards[0].ChangePct <= 1.9 || snapshot.Boards[0].ChangePct >= 2.1 {
		t.Fatalf("change pct: %+v", snapshot.Boards[0])
	}
	w = request(t, h, http.MethodGet, "/api/market-context?as_of=2026-10-16")
	if w.Code != http.StatusOK {
		t.Fatalf("selected date: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/market-context?as_of=bad", "/api/market-context?as_of=2026-10-19&as_of=2026-10-16", "/api/market-context?x=1"} {
		if w := request(t, h, http.MethodGet, path); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid query accepted: %s %d", path, w.Code)
		}
	}
	if w := request(t, h, http.MethodPost, "/api/market-context"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write method accepted: %d", w.Code)
	}
}

func TestMarketContextAPIMissingDatabase(t *testing.T) {
	c := config.Defaults()
	c.DataDir = t.TempDir()
	c.RequireCIFS = false
	h := newHandler(c, &testReader{}, nil)
	w := request(t, h, http.MethodGet, "/api/market-context")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing context database: %d %s", w.Code, w.Body)
	}
}
