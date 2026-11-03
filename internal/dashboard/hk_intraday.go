package dashboard

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func (h *handler) serveHKMinutes(w http.ResponseWriter, r *http.Request) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values) != 2 || len(values["code"]) != 1 || len(values["period"]) != 1 || !config.ValidHKStockCode(values.Get("code")) {
		writeError(w, r, http.StatusBadRequest, "one HK stock code and period are required")
		return
	}
	period, err := strconv.Atoi(values.Get("period"))
	if err != nil || !market.MinutePeriod(period) || strconv.Itoa(period) != values.Get("period") {
		writeError(w, r, http.StatusBadRequest, "period must be 5, 15, 30 or 60")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	db, err := storage.OpenHKMarketView(h.config.DataDir, h.config.RequireCIFS)
	if err != nil {
		h.readError(w, r, ctx, err)
		return
	}
	defer db.Close()
	snapshot, err := db.LoadHKMinutes(ctx, values.Get("code"), period, h.config.HKIntraday)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "HK minute history not found")
		return
	}
	if err != nil {
		h.readError(w, r, ctx, err)
		return
	}
	writeJSON(w, r, http.StatusOK, snapshot)
}
