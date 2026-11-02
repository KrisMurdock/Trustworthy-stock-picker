package market

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type hkUniversePage struct {
	total int
	codes []string
}

func hkUniverseServer(t *testing.T, pages map[int]hkUniversePage) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fs") != "b:DLMK0146,b:DLMK0144" {
			t.Errorf("unexpected HK Connect board filter: %q", r.URL.Query().Get("fs"))
		}
		page, err := strconv.Atoi(r.URL.Query().Get("pn"))
		if err != nil {
			t.Errorf("invalid page: %v", err)
			http.Error(w, "bad page", http.StatusBadRequest)
			return
		}
		data, ok := pages[page]
		if !ok {
			data = hkUniversePage{total: pages[1].total}
		}
		rows := make([]string, 0, len(data.codes))
		for _, code := range data.codes {
			name := "股票" + code
			if code == "bad" {
				name = "bad"
			}
			rows = append(rows, fmt.Sprintf(`{"f12":%q,"f14":%q,"f13":128}`, code, name))
		}
		fmt.Fprintf(w, `{"rc":0,"data":{"total":%d,"diff":[%s]}}`, data.total, strings.Join(rows, ","))
	}))
}

func TestHKConnectUniversePaginatesAndNormalizesCodes(t *testing.T) {
	codes := make([]string, 0, 102)
	for i := 1; i <= 102; i++ {
		codes = append(codes, fmt.Sprintf("%05d", i))
	}
	server := hkUniverseServer(t, map[int]hkUniversePage{
		1: {total: 102, codes: codes[:100]},
		2: {total: 102, codes: codes[100:]},
	})
	defer server.Close()
	client := NewHKConnectUniverseClient(2*time.Second, time.Millisecond, server.URL)
	client.attempts = 1
	universe, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if universe.Market != "hk_connect" || universe.Total != 102 || len(universe.Stocks) != 102 {
		t.Fatalf("unexpected universe: %+v", universe)
	}
	if universe.Stocks[0].Code != "hk00001" || universe.Stocks[101].Code != "hk00102" {
		t.Fatalf("codes not normalized and sorted: first=%s last=%s", universe.Stocks[0].Code, universe.Stocks[101].Code)
	}
}

func TestHKConnectUniverseRejectsIncompleteOrChangingPages(t *testing.T) {
	tests := []struct {
		name  string
		pages map[int]hkUniversePage
	}{
		{name: "incomplete", pages: map[int]hkUniversePage{1: {total: 2, codes: []string{"00700"}}}},
		{name: "changing total", pages: map[int]hkUniversePage{
			1: {total: 101, codes: func() []string {
				out := make([]string, 100)
				for i := range out {
					out[i] = fmt.Sprintf("%05d", i+1)
				}
				return out
			}()},
			2: {total: 102, codes: []string{"00102"}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := hkUniverseServer(t, test.pages)
			defer server.Close()
			client := NewHKConnectUniverseClient(2*time.Second, time.Millisecond, server.URL)
			client.attempts = 1
			if _, err := client.Discover(context.Background()); err == nil {
				t.Fatal("invalid HK Connect pagination was accepted")
			}
		})
	}
}

func TestHKConnectUniverseSkipsInvalidAndDuplicateRows(t *testing.T) {
	server := hkUniverseServer(t, map[int]hkUniversePage{1: {total: 2, codes: []string{"00700", "00700", "bad"}}})
	defer server.Close()
	client := NewHKConnectUniverseClient(2*time.Second, time.Millisecond, server.URL)
	client.attempts = 1
	universe, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if universe.Total != 2 || len(universe.Stocks) != 1 || universe.ExcludedOther != 1 || universe.Stocks[0].Code != "hk00700" {
		t.Fatalf("unexpected filtered universe: %+v", universe)
	}
}

func TestSinaHKConnectUniverseMergesBoards(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if quoted := r.URL.Query().Get("q"); quoted != "" {
			lines := []string{}
			for _, code := range strings.Split(quoted, ",") {
				kind := "GP"
				if code == "hk02800" {
					kind = "GP-FUND"
				}
				lines = append(lines, fmt.Sprintf(`v_%s="100~测试~%s~1~1~1~1~1~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~0~%s";`, code, strings.TrimPrefix(code, "hk"), kind))
			}
			fmt.Fprint(w, strings.Join(lines, "\n"))
			return
		}
		node := r.URL.Query().Get("node")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		rows := []string{}
		switch {
		case node == "hgt_hk" && page == 1:
			rows = []string{`{"symbol":"02800","name":"盈富基金"}`, `{"symbol":"00700","name":"腾讯控股"}`, `{"symbol":"00388","name":"香港交易所"}`}
		case node == "sgt_hk" && page == 1:
			rows = []string{`{"symbol":"00700","name":"腾讯控股"}`, `{"symbol":"01810","name":"小米集团-W"}`, `{"symbol":"bad","name":"无效"}`}
		}
		fmt.Fprintf(w, `[%s]`, strings.Join(rows, ","))
	}))
	defer server.Close()
	client := NewSinaHKConnectUniverseClient(2*time.Second, time.Millisecond, server.URL, server.URL)
	universe, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if universe.Source != "sina" || universe.Total != 3 || len(universe.Stocks) != 3 || universe.ExcludedOther != 2 {
		t.Fatalf("unexpected Sina HK Connect universe: %+v", universe)
	}
	if universe.Stocks[0].Code != "hk00388" || universe.Stocks[2].Code != "hk01810" {
		t.Fatalf("unexpected Sina HK Connect ordering: %+v", universe.Stocks)
	}
}

func TestSinaHKConnectUniverseRejectsEmptyBoard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	client := NewSinaHKConnectUniverseClient(2*time.Second, time.Millisecond, server.URL, server.URL)
	if _, err := client.Discover(context.Background()); err == nil {
		t.Fatal("empty Sina HK Connect board accepted")
	}
}

func TestSinaHKConnectUniverseRejectsUnclassifiedQuotes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "" {
			fmt.Fprint(w, `v_hk00700="100~腾讯控股~00700~1~1~1~1~1";`)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			fmt.Fprint(w, `[{"symbol":"00700","name":"腾讯控股"}]`)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	client := NewSinaHKConnectUniverseClient(2*time.Second, time.Millisecond, server.URL, server.URL)
	if _, err := client.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "missing_hk_connect_quote_type") {
		t.Fatalf("unclassified quote was accepted: %v", err)
	}
}
