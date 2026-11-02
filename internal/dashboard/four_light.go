package dashboard

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const fourLightHistoryMaxDays = 366 * 5

type fourLightSummaryView struct {
	Version         int                       `json:"schema_version"`
	Rule            string                    `json:"rule"`
	Approximation   bool                      `json:"approximation"`
	AlignmentStatus string                    `json:"alignment_status"`
	Run             storage.FourLightRun      `json:"run"`
	First           []storage.FourLightRecord `json:"first_all_red"`
	Continuing      []storage.FourLightRecord `json:"continuing_all_red"`
	Exited          []storage.FourLightRecord `json:"exit_all_red"`
	Unknown         []storage.FourLightRecord `json:"unknown_records"`
}

type fourLightHistoryView struct {
	Version         int                       `json:"schema_version"`
	Rule            string                    `json:"rule"`
	Approximation   bool                      `json:"approximation"`
	AlignmentStatus string                    `json:"alignment_status"`
	Code            string                    `json:"code"`
	From            string                    `json:"from"`
	Through         string                    `json:"through"`
	Records         []storage.FourLightRecord `json:"records"`
}

func (h *handler) serveFourLight(w http.ResponseWriter, r *http.Request) {
	if h.fourLight == nil {
		writeError(w, r, http.StatusServiceUnavailable, "four-light archive is unavailable")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid four-light query")
		return
	}
	asOf := ""
	if dates, present := values["as_of"]; present {
		if len(dates) != 1 || !validFourLightDate(dates[0]) {
			writeError(w, r, http.StatusBadRequest, "as_of must be one date in YYYY-MM-DD format")
			return
		}
		asOf = dates[0]
	}
	for key := range values {
		if key != "as_of" {
			writeError(w, r, http.StatusBadRequest, "unsupported four-light parameter")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	summary, err := h.fourLight.LatestFourLightSummary(ctx, asOf, strategy.FourLightRule)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "four-light signals not found")
		return
	}
	if err != nil {
		h.fourLightError(w, r, ctx, err)
		return
	}
	writeJSON(w, r, http.StatusOK, fourLightSummaryView{Version: 1, Rule: strategy.FourLightRule,
		Approximation: true, AlignmentStatus: strategy.FourLightAlignmentUnverified, Run: summary.Run,
		First: summary.First, Continuing: summary.Continuing, Exited: summary.Exited, Unknown: summary.Unknown})
}

func (h *handler) serveFourLightHistory(w http.ResponseWriter, r *http.Request) {
	if h.fourLight == nil {
		writeError(w, r, http.StatusServiceUnavailable, "four-light archive is unavailable")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid four-light history query")
		return
	}
	codes := values["code"]
	if len(codes) != 1 || !config.ValidCode(codes[0]) {
		writeError(w, r, http.StatusBadRequest, "one stock code is required")
		return
	}
	from, err := requiredFourLightDate(values, "from")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "from must be one date in YYYY-MM-DD format")
		return
	}
	through, err := requiredFourLightDate(values, "through")
	if err != nil || from > through {
		writeError(w, r, http.StatusBadRequest, "through must be one date on or after from")
		return
	}
	start, _ := time.Parse("2006-01-02", from)
	end, _ := time.Parse("2006-01-02", through)
	if end.Sub(start) > fourLightHistoryMaxDays*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "four-light history range must not exceed five years")
		return
	}
	rule := strategy.FourLightRule
	if rules, present := values["rule"]; present {
		if len(rules) != 1 || rules[0] != strategy.FourLightRule {
			writeError(w, r, http.StatusBadRequest, "unsupported four-light rule")
			return
		}
		rule = rules[0]
	}
	for key := range values {
		if key != "code" && key != "from" && key != "through" && key != "rule" {
			writeError(w, r, http.StatusBadRequest, "unsupported four-light history parameter")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	records, err := h.fourLight.FourLightHistory(ctx, codes[0], rule, from, through)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "four-light history not found")
		return
	}
	if err != nil {
		h.fourLightError(w, r, ctx, err)
		return
	}
	writeJSON(w, r, http.StatusOK, fourLightHistoryView{Version: 1, Rule: rule, Approximation: true,
		AlignmentStatus: strategy.FourLightAlignmentUnverified, Code: codes[0], From: from, Through: through, Records: records})
}

func (h *handler) fourLightError(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
	status, message := http.StatusServiceUnavailable, "four-light data is temporarily unavailable"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		status, message = http.StatusGatewayTimeout, "four-light request timed out"
	}
	h.log.Warn("Four-light data request failed", "status", status)
	writeError(w, r, status, message)
}

func requiredFourLightDate(values url.Values, key string) (string, error) {
	dates := values[key]
	if len(dates) != 1 || !validFourLightDate(dates[0]) {
		return "", errors.New("invalid four-light date")
	}
	return dates[0], nil
}

func validFourLightDate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	date, err := time.Parse("2006-01-02", value)
	return err == nil && date.Format("2006-01-02") == value
}
