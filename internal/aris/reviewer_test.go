package aris

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReviewProtocolAndIdentity(t *testing.T) {
	for _, body := range []string{`{"schema_version":1,"verdict":"pass","findings":[],"model":"reviewer","model_family":"other"}`, `{"schema_version":1,"verdict":"veto","findings":[],"model":"reviewer","model_family":"other"}`, `{"schema_version":1,"verdict":"pass","verdict":"veto"}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing auth")
				}
				w.Write([]byte(body))
			}))
			defer srv.Close()
			reviewer, e := NewHTTPReviewer(srv.URL, "test-token", "reviewer", "other")
			if e != nil {
				t.Fatal(e)
			}
			reviewer.client = srv.Client()
			result, e := reviewer.Review(context.Background(), ReviewInput{Model: "reviewer"})
			if body[0] == '{' && len(body) > 40 && body == `{"schema_version":1,"verdict":"pass","findings":[],"model":"reviewer","model_family":"other"}` {
				if e != nil || result.ResponseHash == "" {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("accepted malformed review")
			}
		})
	}
	if _, e := NewHTTPReviewer("http://localhost/", "", "reviewer", "other"); e == nil {
		t.Fatal("allowed plaintext")
	}
}

type fixedReviewer struct {
	calls  int
	result ReviewResult
}

func (f *fixedReviewer) Review(context.Context, ReviewInput) (ReviewResult, error) {
	f.calls++
	return f.result, nil
}
func TestReviewCacheBudgetAndCrossFamily(t *testing.T) {
	a, _ := New(t.TempDir(), "test-build", nil)
	b := configured()
	b.Contract.Review = ReviewPolicy{Mode: "advisory", Backend: "http", Model: "reviewer", Family: "same", AuthorFamily: "same", CrossFamily: true}
	b.Contract.Budget.ReviewCalls = 1
	s, e := a.Run(context.Background(), b)
	if e != nil {
		t.Fatal(e)
	}
	dir, _ := a.dir("test")
	f := &fixedReviewer{result: ReviewResult{Version: 1, Verdict: "pass", Model: "reviewer", Family: "same"}}
	a.Reviewer = f
	for i := 0; i < 2; i++ {
		v, e := a.review(context.Background(), dir, b, &s)
		if e != nil || v != "concern" || s.Review.Independent {
			t.Fatalf("%s %v", v, e)
		}
	}
	if f.calls != 1 || s.ReviewCalls != 1 {
		t.Fatal("cache charged twice")
	}
}

func TestReviewCancellation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	reviewer, e := NewHTTPReviewer(srv.URL, "", "reviewer", "other")
	if e != nil {
		t.Fatal(e)
	}
	reviewer.client = srv.Client()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = reviewer.Review(ctx, ReviewInput{Model: "reviewer"}); e == nil {
		t.Fatal("ignored cancellation")
	}
}
