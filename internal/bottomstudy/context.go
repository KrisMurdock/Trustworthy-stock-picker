package bottomstudy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/indicators"
	"stock-watcher/internal/market"
	"strings"
	"time"
)

// MarketContext is a separately frozen data source. A current classification is
// explicitly retrospective; its retrieval date is never an effective date.
type MarketContext struct {
	Version         int             `json:"schema_version"`
	StockSnapshotID string          `json:"stock_snapshot_id"`
	Basis           string          `json:"classification_basis"`
	ObservedAt      string          `json:"classification_observed_at"`
	From            string          `json:"from"`
	Through         string          `json:"through"`
	Memberships     []Membership    `json:"memberships"`
	Indices         []ContextIndex  `json:"indices"`
	Sources         []ContextSource `json:"sources"`
}

type Membership struct {
	StockCode  string `json:"stock_code"`
	SectorCode string `json:"sector_code"`
	SectorName string `json:"sector_name"`
	ObservedAt string `json:"observed_at"`
	From       string `json:"effective_from,omitempty"`
	Through    string `json:"effective_through,omitempty"` // Inclusive.
}

type ContextIndex struct {
	Code   string       `json:"code"`
	Name   string       `json:"name"`
	Kind   string       `json:"kind"`
	Source string       `json:"source"`
	Bars   []market.Bar `json:"bars"`
}

type ContextSource struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	SHA   string `json:"file_sha256"`
}

type ContextFeature struct {
	Date         string  `json:"date"`
	Close        float64 `json:"close"`
	MA5          float64 `json:"ma5"`
	MA20         float64 `json:"ma20"`
	RSI          float64 `json:"rsi14"`
	Histogram    float64 `json:"macd_histogram"`
	RSIRising    bool    `json:"rsi_rising"`
	MACDRising   bool    `json:"macd_rising"`
	RecentBottom bool    `json:"bottom_event_in_last_5_sessions"`
	Return5      float64 `json:"return_5_sessions_pct"`
	TrendScore   float64 `json:"trend_score"`
	MarketScore  float64 `json:"market_score"`
}

type SignalContext struct {
	Known          bool             `json:"known"`
	Missing        string           `json:"missing,omitempty"`
	SectorCode     string           `json:"sector_code,omitempty"`
	SectorName     string           `json:"sector_name,omitempty"`
	SectorEligible bool             `json:"sector_eligible"`
	SectorScore    float64          `json:"sector_score"`
	MarketScore    float64          `json:"market_score"`
	Sector         *ContextFeature  `json:"sector,omitempty"`
	Markets        []ContextFeature `json:"markets,omitempty"` // Shanghai, Shenzhen, ChiNext.
}

var marketCodes = []string{"sh000001", "sz399001", "sz399006"}

func isoDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func validateContext(c MarketContext, s experiment.Snapshot, o GridOptions) error {
	if c.Version != 1 || c.StockSnapshotID != s.ID || !isoDate(c.From) || !isoDate(c.Through) || c.From > o.From || c.Through < o.Through || c.From >= c.Through {
		return errors.New("context requires version 1, matching stock snapshot and covering dates")
	}
	if c.Basis != "current_only" && c.Basis != "dated" {
		return errors.New("context classification basis must be current_only or dated")
	}
	if _, err := time.Parse(time.RFC3339Nano, c.ObservedAt); err != nil {
		return errors.New("context requires classification observation timestamp")
	}
	configCopy := config.Defaults()
	configCopy.Calendar.ValidFrom, configCopy.Calendar.ValidThrough, configCopy.Calendar.Holidays = s.Calendar.From, s.Calendar.Through, s.Calendar.Holidays
	cal := calendar.New(configCopy)
	calendarFrom, calendarThrough := max(c.From, s.Calendar.From), min(o.Through, s.Calendar.Through)
	if calendarFrom > calendarThrough {
		return errors.New("context has no validated trading calendar coverage")
	}
	expectedDates, err := cal.TradingDays(calendarFrom, calendarThrough)
	if err != nil {
		return err
	}
	indices := map[string]ContextIndex{}
	for _, h := range c.Indices {
		if h.Code == "" || h.Name == "" || h.Source == "" || indices[h.Code].Code != "" || h.Kind != "market" && h.Kind != "sector" || len(h.Bars) == 0 || len(h.Bars) > market.MaxHistoryBars {
			return errors.New("invalid or duplicate context index")
		}
		previous := ""
		dates := map[string]bool{}
		for _, b := range h.Bars {
			date, err := time.Parse(time.DateOnly, b.Date)
			if err != nil || date.Weekday() == time.Saturday || date.Weekday() == time.Sunday || b.Date <= previous || b.Date < c.From || b.Date > c.Through {
				return errors.New("invalid context bar dates")
			}
			for _, v := range []float64{b.Open, b.Close, b.Low, b.High, b.Volume} {
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
					return errors.New("invalid context numeric value")
				}
			}
			if b.Low <= 0 || b.Low > math.Min(b.Open, b.Close) || b.High < math.Max(b.Open, b.Close) {
				return errors.New("invalid context OHLC")
			}
			previous = b.Date
			dates[b.Date] = true
		}
		for _, date := range expectedDates {
			if !dates[date] {
				return fmt.Errorf("context index %s is missing trading session %s", h.Code, date)
			}
		}
		indices[h.Code] = h
	}
	for _, code := range marketCodes {
		if h, ok := indices[code]; !ok || h.Kind != "market" {
			return fmt.Errorf("context requires market index %s", code)
		}
	}
	knownStocks := map[string]bool{}
	for _, stock := range s.Stocks {
		knownStocks[stock.Code] = true
	}
	members := map[string][]Membership{}
	for _, m := range c.Memberships {
		if !knownStocks[m.StockCode] || indices[m.SectorCode].Kind != "sector" || indices[m.SectorCode].Name != m.SectorName {
			return errors.New("invalid context membership or sector reference")
		}
		if _, err := time.Parse(time.RFC3339Nano, m.ObservedAt); err != nil {
			return errors.New("membership requires observation timestamp")
		}
		if c.Basis == "current_only" && (len(members[m.StockCode]) > 0 || m.From != "" || m.Through != "") {
			return errors.New("current classification cannot imply historical effective dates")
		}
		if c.Basis == "dated" && (!isoDate(m.From) || !isoDate(m.Through) || m.From > m.Through) {
			return errors.New("dated membership requires a valid inclusive interval")
		}
		for _, old := range members[m.StockCode] {
			if m.From <= old.Through && old.From <= m.Through {
				return errors.New("overlapping membership intervals")
			}
		}
		members[m.StockCode] = append(members[m.StockCode], m)
	}
	return nil
}

func contextFeatures(ctx context.Context, bars []market.Bar) (map[string]ContextFeature, error) {
	signals, err := detect(ctx, bars)
	if err != nil {
		return nil, err
	}
	out := map[string]ContextFeature{}
	var previous indicators.Metrics
	for i := 34; i < len(bars); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, err := indicators.Score(bars[:i+1])
		if err != nil {
			return nil, err
		}
		f := ContextFeature{Date: bars[i].Date, Close: bars[i].Close, MA5: m.MA5, MA20: m.MA20, RSI: m.RSI14, Histogram: 2 * (m.DIF - m.DEA), RSIRising: m.RSI14 > previous.RSI14, MACDRising: m.DIF-m.DEA > previous.DIF-previous.DEA, Return5: (bars[i].Close/bars[i-5].Close - 1) * 100, TrendScore: float64(m.Score)}
		for j := max(warmup-1, i-4); j <= i; j++ {
			f.RecentBottom = f.RecentBottom || len(signals[j].Events) > 0
		}
		for _, on := range []bool{f.Close >= f.MA20, f.MA5 >= f.MA20, f.MACDRising, f.RSI >= 50} {
			if on {
				f.MarketScore += 25
			}
		}
		if i >= warmup-1 {
			out[f.Date] = f
		}
		previous = m
	}
	return out, nil
}

type contextLookup struct {
	basis    string
	members  map[string][]Membership
	features map[string]map[string]ContextFeature
}

func buildContext(ctx context.Context, c MarketContext, through string) (contextLookup, error) {
	l := contextLookup{c.Basis, map[string][]Membership{}, map[string]map[string]ContextFeature{}}
	for _, m := range c.Memberships {
		l.members[m.StockCode] = append(l.members[m.StockCode], m)
	}
	for _, h := range c.Indices {
		end := sort.Search(len(h.Bars), func(i int) bool { return h.Bars[i].Date > through })
		f, err := contextFeatures(ctx, h.Bars[:end])
		if err != nil {
			return l, err
		}
		l.features[h.Code] = f
	}
	return l, nil
}

func (l contextLookup) at(stock, date string) SignalContext {
	d := SignalContext{Missing: "missing_membership"}
	for _, m := range l.members[stock] {
		if l.basis == "current_only" || m.From <= date && date <= m.Through {
			d.SectorCode, d.SectorName = m.SectorCode, m.SectorName
			break
		}
	}
	if d.SectorCode == "" {
		return d
	}
	sector, ok := l.features[d.SectorCode][date]
	if !ok {
		d.Missing = "missing_sector_history_or_warmup"
		return d
	}
	primary := "sz399001"
	if strings.HasPrefix(stock, "sh") {
		primary = "sh000001"
	} else if strings.HasPrefix(stock, "sz30") {
		primary = "sz399006"
	}
	meanScore, meanReturn, primaryScore := 0.0, 0.0, 0.0
	for _, code := range marketCodes {
		f, ok := l.features[code][date]
		if !ok {
			d.Missing = "missing_market_history_or_warmup"
			return d
		}
		d.Markets = append(d.Markets, f)
		meanScore += f.MarketScore / 3
		meanReturn += f.Return5 / 3
		if code == primary {
			primaryScore = f.MarketScore
		}
	}
	d.Known, d.Missing, d.Sector = true, "", &sector
	d.MarketScore = .5*meanScore + .5*primaryScore
	d.SectorEligible = sector.RecentBottom && (sector.MACDRising || sector.RSIRising) || sector.Close >= sector.MA20 && sector.MA5 >= sector.MA20 && sector.MACDRising
	for _, on := range []bool{sector.MACDRising, sector.RSIRising, sector.Close >= sector.MA5, sector.Return5 >= meanReturn, sector.RecentBottom} {
		if on {
			d.SectorScore += 20
		}
	}
	return d
}
