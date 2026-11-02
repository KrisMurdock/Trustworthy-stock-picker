package market

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestUniverseDiscoverFiltersRiskAndDelistedStocks(t *testing.T) {
	rows := map[string][][]map[string]any{
		"sh_a": {{
			{"symbol": "sh600000", "code": "600000", "name": "浦发银行"},
			{"symbol": "sh600001", "code": "600001", "name": "*ST测试"},
			{"symbol": "sh600002", "code": "600002", "name": "退市测试"},
			{"symbol": "sh900901", "code": "900901", "name": "沪市B股"},
			{"symbol": "sh688001", "code": "688001", "name": "科创测试"},
			{"symbol": "sh600003", "code": "600003", "name": "普通股份"},
		}},
		"sz_a": {{
			{"symbol": "sz000001", "code": "000001", "name": "平安银行"},
			{"symbol": "sz300001", "code": "300001", "name": "创业测试"},
			{"symbol": "sz200001", "code": "200001", "name": "深市B股"},
			{"symbol": "sz300002", "code": "300002", "name": "ST测试"},
		}},
	}
	server := universeServer(t, map[string]int{"sh_a": 6, "sz_a": 4}, rows)
	defer server.Close()

	client := NewUniverseClient(2*time.Second, time.Millisecond, server.URL+"/list", server.URL+"/count")
	defer client.Close()
	universe, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if universe.Total != 10 || universe.ExcludedST != 2 || universe.ExcludedDelisted != 1 || universe.ExcludedOther != 2 {
		t.Fatalf("unexpected universe counters: %+v", universe)
	}
	want := []string{"sh600000", "sh600003", "sh688001", "sz000001", "sz300001"}
	if len(universe.Stocks) != len(want) {
		t.Fatalf("unexpected eligible stocks: %+v", universe.Stocks)
	}
	for i, code := range want {
		if universe.Stocks[i].Code != code {
			t.Fatalf("eligible stock %d = %s, want %s", i, universe.Stocks[i].Code, code)
		}
	}
}

func TestUniverseDiscoverReadsEveryPage(t *testing.T) {
	shPages := [][]map[string]any{}
	for page := 1; page <= 2; page++ {
		rows := []map[string]any{}
		start, end := 1, 100
		if page == 2 {
			start, end = 101, 150
		}
		for i := start; i <= end; i++ {
			rows = append(rows, map[string]any{"symbol": fmt.Sprintf("sh6%05d", i), "code": fmt.Sprintf("6%05d", i), "name": fmt.Sprintf("股票%d", i)})
		}
		shPages = append(shPages, rows)
	}
	rows := map[string][][]map[string]any{
		"sh_a": shPages,
		"sz_a": {{map[string]any{"symbol": "sz000001", "code": "000001", "name": "平安银行"}}},
	}
	server := universeServer(t, map[string]int{"sh_a": 150, "sz_a": 1}, rows)
	defer server.Close()
	client := NewUniverseClient(2*time.Second, time.Millisecond, server.URL+"/list", server.URL+"/count")
	defer client.Close()
	universe, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(universe.Stocks) != 151 || universe.Total != 151 {
		t.Fatalf("unexpected paginated universe: total=%d stocks=%d", universe.Total, len(universe.Stocks))
	}
}

func TestUniverseDiscoverRejectsInvalidAndIncompleteCounts(t *testing.T) {
	for name, tc := range map[string]struct {
		counts map[string]int
		rows   map[string][][]map[string]any
	}{
		"zero total": {map[string]int{"sh_a": 0, "sz_a": 1}, map[string][][]map[string]any{}},
		"incomplete": {
			map[string]int{"sh_a": 5, "sz_a": 1},
			map[string][][]map[string]any{
				"sh_a": {{map[string]any{"symbol": "sh600000", "code": "600000", "name": "浦发银行"}}},
				"sz_a": {{map[string]any{"symbol": "sz000001", "code": "000001", "name": "平安银行"}}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := universeServer(t, tc.counts, tc.rows)
			defer server.Close()
			client := NewUniverseClient(2*time.Second, time.Millisecond, server.URL+"/list", server.URL+"/count")
			defer client.Close()
			if _, err := client.Discover(context.Background()); err == nil {
				t.Fatal("invalid universe unexpectedly succeeded")
			}
		})
	}
}

func universeServer(t *testing.T, counts map[string]int, rows map[string][][]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node := r.URL.Query().Get("node")
		count, ok := counts[node]
		if !ok {
			t.Fatalf("unexpected universe node: %q", node)
		}
		switch r.URL.Path {
		case "/count":
			_ = json.NewEncoder(w).Encode(strconv.Itoa(count))
		case "/list":
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil || page < 1 {
				t.Fatalf("invalid page: %q", r.URL.Query().Get("page"))
			}
			items := []map[string]any{}
			if pages := rows[node]; page <= len(pages) {
				items = pages[page-1]
			}
			_ = json.NewEncoder(w).Encode(items)
		default:
			t.Fatalf("unexpected universe path: %q", r.URL.Path)
		}
	}))
}

func TestUniverseCode(t *testing.T) {
	for _, code := range []string{"sh600000", "sh688001", "sz000001", "sz300001"} {
		if got := universeCode(code); got != code {
			t.Fatalf("universeCode(%s) = %q", code, got)
		}
	}
	for _, code := range []string{"sh900901", "sz200001", "bj830799", "sh000001", "sz600000", "sh60000", ""} {
		if got := universeCode(code); got != "" {
			t.Fatalf("universeCode(%s) = %q, want empty", code, got)
		}
	}
}

func TestIsSTStockName(t *testing.T) {
	for _, name := range []string{"ST测试", "*ST测试", "SST测试", "S*ST测试", "XDST测试", "XD *ST测试", "XRST测试", "DRS*ST测试"} {
		if !IsSTStockName(name) {
			t.Fatalf("%s was not recognized as ST", name)
		}
	}
	for _, name := range []string{"浦发银行", "N新股", "中国石化", "XD浦发银行", "DR中国石化"} {
		if IsSTStockName(name) {
			t.Fatalf("%s was incorrectly recognized as ST", name)
		}
	}
	for _, name := range []string{"退市测试", "某某退"} {
		if !isDelistedName(name) {
			t.Fatalf("%s was not recognized as delisted", name)
		}
	}
}
