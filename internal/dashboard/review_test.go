package dashboard

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/review"
)

type reviewFake struct {
	day      review.Day
	event    review.Event
	err      error
	wait     bool
	asOf     string
	code     string
	revision string
}

func (f *reviewFake) Day(ctx context.Context, asof, revision string) (review.Day, error) {
	f.asOf, f.revision = asof, revision
	if f.wait {
		<-ctx.Done()
		return review.Day{}, ctx.Err()
	}
	return f.day, f.err
}

func (f *reviewFake) Event(ctx context.Context, asof, code, revision string) (review.Event, error) {
	f.asOf, f.code, f.revision = asof, code, revision
	if f.wait {
		<-ctx.Done()
		return review.Event{}, ctx.Err()
	}
	return f.event, f.err
}

func reviewHandler(t *testing.T, fake *reviewFake) (*handler, *httptest.ResponseRecorder) {
	t.Helper()
	h := newHandler(config.Defaults(), &testReader{}, nil).(*handler)
	h.review = fake
	return h, httptest.NewRecorder()
}

func TestReviewAPIsReadAndValidateParameters(t *testing.T) {
	revision := strings.Repeat("a", 64)
	fake := &reviewFake{day: review.Day{Version: 1, AsOf: "2026-10-12", Revision: revision}, event: review.Event{EventSummary: review.EventSummary{Code: "sz000001"}}}
	h, w := reviewHandler(t, fake)
	r := httptest.NewRequest(http.MethodGet, "/api/review?as_of=2026-10-12&revision="+revision, nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || fake.asOf != "2026-10-12" || fake.revision != revision || !strings.Contains(w.Body.String(), `"schema_version":1`) {
		t.Fatalf("day review failed: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/review/event?as_of=2026-10-12&code=sz000001", nil))
	if w.Code != http.StatusOK || fake.code != "sz000001" || !strings.Contains(w.Body.String(), `"code":"sz000001"`) {
		t.Fatalf("event review failed: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{
		"/api/review",
		"/api/review?as_of=bad",
		"/api/review?as_of=2026-10-12&revision=short",
		"/api/review?as_of=2026-10-12&x=1",
		"/api/review?as_of=2026-10-12&as_of=2026-10-13",
		"/api/review/event?as_of=2026-10-12",
		"/api/review/event?as_of=2026-10-12&code=bad",
		"/api/review/event?as_of=2026-10-12&code=sz000001&x=1",
	} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid query accepted: %s %d", path, w.Code)
		}
	}
	if w = request(t, h, http.MethodPost, "/api/review?as_of=2026-10-12"); w.Code != http.StatusMethodNotAllowed {
		t.Fatal("review write method allowed")
	}
}

func TestReviewAPIErrorsAndTimeout(t *testing.T) {
	h, _ := reviewHandler(t, &reviewFake{err: fs.ErrNotExist})
	w := request(t, h, http.MethodGet, "/api/review?as_of=2026-10-12")
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("not found mismatch: %d %s", w.Code, w.Body)
	}
	h, _ = reviewHandler(t, &reviewFake{err: errors.New("private database failure")})
	w = request(t, h, http.MethodGet, "/api/review?as_of=2026-10-12")
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("internal error leaked: %d %s", w.Code, w.Body)
	}
	h, _ = reviewHandler(t, &reviewFake{wait: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/review?as_of=2026-10-12", nil).WithContext(ctx)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout mismatch: %d %s", w.Code, w.Body)
	}
}
