package dashboard

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"io/fs"
	"net/http"
	"net/url"
	"time"
)

func (h *handler) serveExperiments(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, 400, "invalid experiment query")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	fail := func(err error) {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, r, 404, "experiment not found")
		} else {
			h.readError(w, r, ctx, err)
		}
	}
	if r.URL.Path == "/api/experiments" && len(query) == 0 {
		records, err := h.experiments.List(ctx)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, r, 200, map[string]any{"schema_version": 1, "experiments": records, "limit": 200})
		return
	}
	ids := query["id"]
	if len(ids) != 1 || !experiment.ValidID(ids[0]) {
		writeError(w, r, 400, "one experiment ID is required")
		return
	}
	if r.URL.Path == "/api/experiments" {
		if len(query) != 1 {
			writeError(w, r, 400, "unsupported experiment parameter")
			return
		}
		record, err := h.experiments.Get(ctx, ids[0])
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, r, 200, record)
		return
	}
	codes, dates := query["code"], query["date"]
	if len(query) != 3 || len(codes) != 1 || !config.ValidCode(codes[0]) || len(dates) != 1 {
		writeError(w, r, 400, "one stock code and signal date are required")
		return
	}
	if date, err := time.Parse("2006-01-02", dates[0]); err != nil || date.Format("2006-01-02") != dates[0] {
		writeError(w, r, 400, "invalid signal date")
		return
	}
	history, err := h.experiments.History(ctx, ids[0], codes[0], dates[0])
	if err != nil {
		fail(err)
		return
	}
	writeJSON(w, r, 200, history)
}
