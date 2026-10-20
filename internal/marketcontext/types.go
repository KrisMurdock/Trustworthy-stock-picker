package marketcontext

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const SnapshotVersion = 1

type InstrumentSnapshot struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Category  string  `json:"category"`
	Source    string  `json:"source"`
	AsOf      string  `json:"as_of"`
	Close     float64 `json:"close"`
	ChangePct float64 `json:"change_pct"`
	MA5       float64 `json:"ma5"`
	MA20      float64 `json:"ma20"`
	Trend     string  `json:"trend"`
	Bars      int     `json:"bars"`
	Stale     bool    `json:"stale"`
}

type Summary struct {
	Industry int `json:"industry"`
	Concept  int `json:"concept"`
	Up       int `json:"up"`
	Down     int `json:"down"`
	Flat     int `json:"flat"`
}

type Snapshot struct {
	Version    int                  `json:"schema_version"`
	AsOf       string               `json:"as_of"`
	SyncedAt   time.Time            `json:"synced_at"`
	Status     string               `json:"status"`
	Total      int                  `json:"total"`
	Complete   int                  `json:"complete"`
	Failed     int                  `json:"failed"`
	Indices    []InstrumentSnapshot `json:"indices"`
	Boards     []InstrumentSnapshot `json:"boards"`
	Summary    Summary              `json:"summary"`
	Warnings   []string             `json:"warnings,omitempty"`
	SyncErrors []string             `json:"sync_errors,omitempty"`
}

// BuildSnapshot projects normalized context rows into the read-only dashboard
// contract. It never substitutes an older bar for the requested date silently:
// stale instruments are returned with stale=true and their actual last date.
func BuildSnapshot(asOf string, instruments []storage.ContextInstrument, bars map[string][]storage.ContextBar, status storage.ContextSyncStatus) Snapshot {
	s := Snapshot{Version: SnapshotVersion, AsOf: asOf, Total: len(instruments), Indices: []InstrumentSnapshot{}, Boards: []InstrumentSnapshot{}}
	if !status.FinishedAt.IsZero() {
		s.SyncedAt = status.FinishedAt
	}
	s.Status = status.Status
	if s.Status == "" {
		s.Status = "missing"
	}
	s.Failed = status.Failed
	if status.AsOf != "" && status.AsOf != asOf && s.Status != "missing" {
		s.Warnings = append(s.Warnings, "市场环境快照日期与所选复盘日期不一致")
	}
	for _, instrument := range instruments {
		view, ok := project(instrument, bars[instrument.Code], asOf)
		if !ok {
			continue
		}
		if instrument.Kind == "index" {
			s.Indices = append(s.Indices, view)
			continue
		}
		s.Boards = append(s.Boards, view)
		categories := strings.Split(instrument.Category, ",")
		for _, category := range categories {
			switch category {
			case "industry":
				s.Summary.Industry++
			case "concept":
				s.Summary.Concept++
			}
		}
		if view.Stale {
			continue
		}
		switch {
		case view.ChangePct > 0.0001:
			s.Summary.Up++
		case view.ChangePct < -0.0001:
			s.Summary.Down++
		default:
			s.Summary.Flat++
		}
	}
	sort.SliceStable(s.Indices, func(i, j int) bool { return indexOrder(s.Indices[i].Code) < indexOrder(s.Indices[j].Code) })
	sort.SliceStable(s.Boards, func(i, j int) bool {
		if s.Boards[i].ChangePct != s.Boards[j].ChangePct {
			return s.Boards[i].ChangePct > s.Boards[j].ChangePct
		}
		return s.Boards[i].Name < s.Boards[j].Name
	})
	for _, item := range append(append([]InstrumentSnapshot{}, s.Indices...), s.Boards...) {
		if !item.Stale && item.AsOf == asOf {
			s.Complete++
		}
	}
	if s.Total == 0 {
		s.Warnings = append(s.Warnings, "尚未同步指数与板块快照")
	}
	return s
}

func project(instrument storage.ContextInstrument, bars []storage.ContextBar, asOf string) (InstrumentSnapshot, bool) {
	if len(bars) == 0 {
		return InstrumentSnapshot{}, false
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	usable := bars[:0]
	for _, bar := range bars {
		if bar.Date <= asOf {
			usable = append(usable, bar)
		}
	}
	if len(usable) == 0 {
		return InstrumentSnapshot{}, false
	}
	bars = usable
	view := InstrumentSnapshot{Code: instrument.Code, Name: instrument.Name, Kind: instrument.Kind, Category: instrument.Category, Source: instrument.Source, AsOf: bars[len(bars)-1].Date, Bars: len(bars)}
	view.Close = bars[len(bars)-1].Close
	if len(bars) > 1 {
		previous := bars[len(bars)-2].Close
		if previous > 0 {
			view.ChangePct = (view.Close/previous - 1) * 100
		}
	}
	view.MA5 = movingAverage(bars, 5)
	view.MA20 = movingAverage(bars, 20)
	view.Trend = trend(view.Close, view.MA5, view.MA20)
	view.Stale = view.AsOf != asOf
	return view, true
}

func movingAverage(bars []storage.ContextBar, count int) float64 {
	if len(bars) < count {
		return 0
	}
	sum := 0.0
	for _, bar := range bars[len(bars)-count:] {
		sum += bar.Close
	}
	return sum / float64(count)
}

func trend(close, ma5, ma20 float64) string {
	if ma5 == 0 || ma20 == 0 {
		return "unknown"
	}
	switch {
	case close > ma5 && ma5 > ma20:
		return "up"
	case close < ma5 && ma5 < ma20:
		return "down"
	default:
		return "mixed"
	}
}

func indexOrder(code string) int {
	switch code {
	case "sh000001":
		return 0
	case "sz399001":
		return 1
	case "sz399006":
		return 2
	case "sh000300":
		return 3
	case "sh000688":
		return 4
	}
	return 100
}

func validateBars(code, asOf string, bars []storage.ContextBar) error {
	if len(bars) == 0 {
		return errors.New("empty context bars")
	}
	previous := ""
	for _, bar := range bars {
		if bar.Code != code {
			return errors.New("context bar code mismatch")
		}
		if _, err := time.Parse("2006-01-02", bar.Date); err != nil || bar.Date <= previous || bar.Date > asOf {
			return errors.New("invalid context bar date order")
		}
		previous = bar.Date
		for _, value := range []float64{bar.Open, bar.Close, bar.High, bar.Low, bar.Volume, bar.Amount} {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return errors.New("invalid context numeric value")
			}
		}
		if bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) {
			return errors.New("invalid context OHLC range")
		}
	}
	return nil
}
