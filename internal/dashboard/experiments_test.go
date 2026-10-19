package dashboard

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type experimentFake struct {
	err  error
	wait bool
}

func (f experimentFake) List(ctx context.Context) ([]experiment.Record, error) {
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []experiment.Record{}, f.err
}
func (f experimentFake) Get(ctx context.Context, id string) (experiment.Record, error) {
	_, err := f.List(ctx)
	return experiment.Record{Version: 1, ID: id}, err
}
func (f experimentFake) History(ctx context.Context, id, code, date string) (market.History, error) {
	_, err := f.List(ctx)
	return market.History{Code: code}, err
}
func TestExperimentAPIReadOnlyErrorsAndBoundaries(t *testing.T) {
	h := newHandler(config.Defaults(), &testReader{}, nil).(*handler)
	h.experiments = experimentFake{}
	id := "0123456789abcdef0123456789abcdef"
	for _, path := range []string{"/api/experiments", "/api/experiments?id=" + id, "/api/experiment-history?id=" + id + "&code=sz000001&date=2026-10-13"} {
		for _, identity := range []string{"", "Bearer ordinary-user", "Bearer administrator"} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", identity)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
		}
		if w := request(t, h, "POST", path); w.Code != 405 {
			t.Fatal("write allowed")
		}
	}
	for _, path := range []string{"/api/experiments?x=1", "/api/experiments?id=../../private", "/api/experiments?id=" + id + "&id=" + id, "/api/experiment-history?id=" + id, "/api/experiment-history?id=" + id + "&code=sz000001&date=2026-02-30", "/api/experiment-history?id=" + id + "&code=sz000001&date=2026-10-13&x=1"} {
		if w := request(t, h, "GET", path); w.Code != 400 {
			t.Fatal("invalid query accepted", path, w.Code)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{fs.ErrNotExist, 404}, {errors.New("private path secret"), 503}, {context.DeadlineExceeded, 504}} {
		h.experiments = experimentFake{err: tc.err}
		w := request(t, h, "GET", "/api/experiments?id="+id)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body)
		}
	}
	h.experiments = experimentFake{wait: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/experiments", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 504 {
		t.Fatal("timeout unhandled")
	}
}
