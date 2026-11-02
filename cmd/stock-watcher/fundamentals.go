package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fundamentals"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type fundamentalsSyncItem struct {
	Code         string `json:"code"`
	Status       string `json:"status"`
	Periods      int    `json:"periods"`
	Inserted     int    `json:"inserted"`
	LatestReport string `json:"latest_report_end,omitempty"`
	FetchedAt    string `json:"fetched_at,omitempty"`
	Error        string `json:"error,omitempty"`
}

type fundamentalsSyncReport struct {
	Command    string                 `json:"command"`
	AsOf       string                 `json:"as_of"`
	Source     string                 `json:"source"`
	WindowFrom string                 `json:"window_from"`
	WindowTo   string                 `json:"window_to"`
	Stocks     int                    `json:"stocks"`
	Succeeded  int                    `json:"succeeded"`
	Failed     int                    `json:"failed"`
	Inserted   int                    `json:"inserted"`
	Results    []fundamentalsSyncItem `json:"results"`
}

type fundamentalsAuditReport struct {
	Command     string                           `json:"command"`
	HasDatabase bool                             `json:"has_database"`
	Audit       storage.FundamentalsAudit        `json:"audit"`
	Sync        []storage.FundamentalsSyncRecord `json:"sync,omitempty"`
}

func runSyncFundamentals(ctx context.Context, c config.Config, log *logging.Logger, asOf, codesCSV string) error {
	cal := calendar.New(c)
	if asOf == "" {
		var err error
		asOf, err = cal.Completed(time.Now())
		if err != nil {
			return err
		}
	}
	from, through, err := fundamentalsReportWindow(asOf)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.Market.TushareToken) == "" {
		return errors.New("sync-fundamentals requires the configured Tushare token")
	}
	stocks, err := fundamentalsStocks(c, codesCSV)
	if err != nil {
		return err
	}
	db, err := storage.OpenFundamentals(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	client := market.NewTushareClient(config.Duration(c.Timeout), config.Duration(c.Market.RequestInterval), c.Market.TushareToken)
	client.Endpoint = c.Market.TushareEndpoint
	defer client.Close()
	log.RegisterSecret(c.Market.TushareToken)

	report := fundamentalsSyncReport{
		Command: "sync-fundamentals", AsOf: asOf, Source: fundamentals.SourceTushare,
		WindowFrom: from, WindowTo: through, Stocks: len(stocks), Results: make([]fundamentalsSyncItem, 0, len(stocks)),
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	for _, stock := range stocks {
		if fetchCtx.Err() != nil {
			break
		}
		series, fetchErr := client.FetchAnnualFundamentals(fetchCtx, stock, from, through, cal.Next)
		if fetchErr != nil {
			item := fundamentalsSyncItem{Code: stock.Code, Status: "failed", Error: fetchErr.Error()}
			report.Failed++
			report.Results = append(report.Results, item)
			if err := db.RecordSync(fetchCtx, storage.FundamentalsSyncRecord{
				Code: stock.Code, Source: fundamentals.SourceTushare, Status: "failed", LastError: fetchErr.Error(),
			}); err != nil {
				return err
			}
			log.Warn("Fundamental synchronization failed", "code", stock.Code, "error", fetchErr.Error())
			continue
		}
		inserted, err := db.AppendSeries(fetchCtx, series)
		if err != nil {
			return err
		}
		latest := series.Periods[len(series.Periods)-1].ReportEnd
		report.Succeeded++
		report.Inserted += inserted
		report.Results = append(report.Results, fundamentalsSyncItem{
			Code: stock.Code, Status: "ok", Periods: len(series.Periods), Inserted: inserted,
			LatestReport: latest, FetchedAt: series.FetchedAt.In(market.Shanghai).Format(time.RFC3339),
		})
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return err
	}
	if err := fetchCtx.Err(); err != nil {
		return err
	}
	if report.Failed > 0 {
		return fmt.Errorf("fundamentals synchronization has %d failed stocks", report.Failed)
	}
	return nil
}

func runAuditFundamentals(ctx context.Context, c config.Config, asOf string) error {
	cal := calendar.New(c)
	if asOf == "" {
		var err error
		asOf, err = cal.Completed(time.Now())
		if err != nil {
			return err
		}
	}
	db, err := storage.OpenFundamentalsView(c.DataDir, c.RequireCIFS)
	if err != nil {
		return err
	}
	defer db.Close()
	audit, err := db.Audit(ctx, asOf)
	if err != nil {
		return err
	}
	sync, err := db.ListSync(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(fundamentalsAuditReport{
		Command: "audit-fundamentals", HasDatabase: db.HasDatabase(), Audit: audit, Sync: sync,
	})
}

func fundamentalsReportWindow(asOf string) (string, string, error) {
	date, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return "", "", errors.New("invalid fundamentals as-of date")
	}
	endYear := date.Year() - 1
	startYear := endYear - fundamentals.MaxWindowYears + 1
	if startYear < 1990 {
		return "", "", errors.New("fundamentals as-of year is too early")
	}
	return fmt.Sprintf("%d-01-01", startYear), fmt.Sprintf("%d-12-31", endYear), nil
}

func fundamentalsStocks(c config.Config, codesCSV string) ([]config.Stock, error) {
	if strings.TrimSpace(codesCSV) == "" {
		return append([]config.Stock(nil), c.Stocks...), nil
	}
	byCode := make(map[string]config.Stock, len(c.Stocks))
	for _, stock := range c.Stocks {
		byCode[stock.Code] = stock
	}
	seen := map[string]bool{}
	stocks := make([]config.Stock, 0)
	for _, part := range strings.Split(codesCSV, ",") {
		code := strings.TrimSpace(part)
		if code == "" || seen[code] {
			continue
		}
		stock, ok := byCode[code]
		if !ok {
			return nil, fmt.Errorf("fundamentals code %s is not in the configured pool", code)
		}
		seen[code] = true
		stocks = append(stocks, stock)
	}
	if len(stocks) == 0 {
		return nil, errors.New("fundamentals code list is empty")
	}
	return stocks, nil
}
