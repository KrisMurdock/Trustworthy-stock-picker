package strategy

import (
	"context"
	"math"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const UpwardWaveModel = "upward_wave_v0"

// UpwardWave is an ex-post path label, never an entry feature or an exit rule.
// Entry is the existing opening-price simulation's fill. Legacy exits do not
// truncate this path. All percentages use percentage points, ratios use [0,1].
type UpwardWave struct {
	Model       string             `json:"model"`
	EntryStatus string             `json:"entry_status"` // entered / not_entered / unknown
	EntryReason string             `json:"entry_reason,omitempty"`
	EntryDate   string             `json:"entry_date,omitempty"`
	EntryPrice  float64            `json:"entry_price,omitempty"`
	ATR14Pct    float64            `json:"signal_atr14_pct"`
	Days        []UpwardWaveDay    `json:"days"`
	Windows     []UpwardWaveWindow `json:"windows"`
}

type UpwardWaveDay struct {
	Day         int     `json:"day"` // entry session is D1
	Date        string  `json:"date"`
	Close       float64 `json:"close"`
	ChangePct   float64 `json:"change_pct"` // D1 versus fill, later versus previous close
	CloseNetPct float64 `json:"close_net_pct"`
	LowNetPct   float64 `json:"low_net_pct"`
}

type UpwardWaveMetrics struct {
	StartDay         int     `json:"start_day"`
	EndDay           int     `json:"end_day"`
	Duration         int     `json:"duration"`
	UpDays           int     `json:"up_days"`
	UpRatio          float64 `json:"up_ratio"`
	LongestUp        int     `json:"longest_up"`
	LongestDown      int     `json:"longest_down"`
	Efficiency       float64 `json:"efficiency"` // signed; never abs(net change)
	EndNetPct        float64 `json:"end_net_pct"`
	MaxLossPct       float64 `json:"max_loss_pct"`       // positive cost-reserved intraday loss from entry
	CloseDrawdownPct float64 `json:"close_drawdown_pct"` // peak-to-close, including entry anchor
}

type UpwardWaveWindow struct {
	Days            int                `json:"days"`
	Status          string             `json:"status"` // success / failed / unknown / not_entered
	Reason          string             `json:"reason,omitempty"`
	Segment         *UpwardWaveMetrics `json:"segment,omitempty"`
	Full            *UpwardWaveMetrics `json:"full,omitempty"`
	PeakCloseNetPct *float64           `json:"peak_close_net_pct,omitempty"`
	GivebackPct     *float64           `json:"giveback_pct,omitempty"` // peak close net minus final close net
}

type UpwardWaveStats struct {
	Days                  int            `json:"days"`
	Selected              int            `json:"selected"`
	SignalDates           int            `json:"signal_dates"`
	Entered               int            `json:"entered"`
	NotEntered            int            `json:"not_entered"`
	EntryUnknown          int            `json:"entry_unknown"`
	Known                 int            `json:"known"`
	Success               int            `json:"success"`
	Failed                int            `json:"failed"`
	Unknown               int            `json:"unknown"` // entered, path unassessable
	UnknownReasons        map[string]int `json:"unknown_reasons"`
	Coverage              *float64       `json:"coverage"`
	SuccessRate           *float64       `json:"success_rate"`
	ConservativeRate      *float64       `json:"conservative_rate"`
	MeanStartDay          *float64       `json:"mean_start_day"` // successful segments only
	MeanDuration          *float64       `json:"mean_duration"`
	MeanSegmentEfficiency *float64       `json:"mean_segment_efficiency"`
	MeanEndNetPct         *float64       `json:"mean_end_net_pct"` // all known full windows
	MeanGivebackPct       *float64       `json:"mean_giveback_pct"`
	MeanMaxLossPct        *float64       `json:"mean_max_loss_pct"`
	WorstMaxLossPct       *float64       `json:"worst_max_loss_pct"`
}

type UpwardWaveSummary struct {
	Model   string            `json:"model"`
	Windows []UpwardWaveStats `json:"windows"`
}

// attachUpwardWaves only labels selected opening-price trades. In development,
// boundary is exclusive: neither prices nor adjustment factors at/after the
// reserved boundary are observed. Each window has its own coverage decision.
func attachUpwardWaves(ctx context.Context, c config.Config, data []Dataset, result *DevelopmentResult, boundary, asof string) error {
	byCode := make(map[string]Dataset, len(data))
	for _, d := range data {
		byCode[d.Raw.Code] = d
	}
	cal := calendar.New(c)
	for i := range result.Trades {
		if err := ctx.Err(); err != nil {
			return err
		}
		t := &result.Trades[i]
		if !t.Selected {
			continue
		}
		d := byCode[t.Code]
		t.UpwardWave = labelUpwardWave(c.Strategy, cal, *t, d, boundary, asof)
	}
	result.UpwardWave = summarizeUpwardWaves(result.Trades)
	return nil
}

func labelUpwardWave(s config.Strategy, cal *calendar.Calendar, t ExperimentTrade, d Dataset, boundary, asof string) *UpwardWave {
	w := &UpwardWave{Model: UpwardWaveModel, EntryStatus: "unknown", ATR14Pct: t.Features.ATR14Pct, Days: []UpwardWaveDay{}, Windows: []UpwardWaveWindow{}}
	entry := t.Outcome.EntryDay
	if entry == nil || entry.Mode != "opening_price" || entry.Version != 1 || !positiveFinite(entry.Fill) || !positiveFinite(entry.BuyCost) || entry.Shares <= 0 || t.Outcome.EntryDate == "" {
		w.EntryReason = "entry_evidence_missing"
		if t.Outcome.EntryDate == "" {
			switch t.Outcome.Status {
			case "entry_skipped", "cash_insufficient", "risk_budget_insufficient":
				w.EntryStatus, w.EntryReason = "not_entered", t.Outcome.Status
			}
		}
		for _, n := range []int{5, 10} {
			w.Windows = append(w.Windows, UpwardWaveWindow{Days: n, Status: w.EntryStatus, Reason: w.EntryReason})
		}
		return w
	}
	w.EntryStatus, w.EntryDate, w.EntryPrice = "entered", t.Outcome.EntryDate, entry.Fill
	p := Plan{EntryPrice: entry.Fill, BuyCost: entry.BuyCost, Shares: entry.Shares}
	raw, adj := d.Raw.Bars, d.Adjusted.Bars
	i := sort.Search(len(raw), func(i int) bool { return raw[i].Date >= t.Date })
	reason := ""
	if i >= len(raw) || i >= len(adj) || raw[i].Date != t.Date || adj[i].Date != t.Date || !positiveFinite(raw[i].Close) || !positiveFinite(adj[i].Close) {
		reason = "signal_data_missing"
	} else if !positiveFinite(w.ATR14Pct) {
		reason = "atr_unknown"
	} else {
		factor, previous, date := adj[i].Close/raw[i].Close, entry.Fill, t.Date
		for day := 1; day <= 10; day++ {
			expected, err := cal.Next(date)
			if err != nil {
				reason = "calendar_missing"
				break
			}
			if boundary != "" && expected >= boundary {
				reason = "reserved_boundary"
				break
			}
			if expected > asof {
				reason = "future_window_incomplete"
				break
			}
			j := i + day
			if j >= len(raw) || j >= len(adj) || raw[j].Date != expected || adj[j].Date != expected {
				reason = "session_missing"
				break
			}
			b, a := raw[j], adj[j]
			if !validWaveBar(b) || !validWaveBar(a) || b.Volume <= 0 || a.Volume <= 0 {
				reason = "bar_invalid_or_suspended"
				break
			}
			if day == 1 && expected != w.EntryDate {
				reason = "entry_date_mismatch"
				break
			}
			// Same conservative adjustment-factor tolerance as legacy research.
			if math.Abs((a.Close/b.Close)/factor-1) > .002 {
				reason = "corporate_action"
				break
			}
			w.Days = append(w.Days, UpwardWaveDay{Day: day, Date: expected, Close: b.Close, ChangePct: (b.Close/previous - 1) * 100, CloseNetPct: netPct(s, p, tickDown(b.Close*(1-s.SlippageRate))), LowNetPct: netPct(s, p, tickDown(b.Low*(1-s.SlippageRate)))})
			previous, date = b.Close, expected
		}
	}
	for _, n := range []int{5, 10} {
		x := UpwardWaveWindow{Days: n, Status: "unknown", Reason: reason}
		if len(w.Days) >= n {
			x = evaluateUpwardWindow(w.Days[:n], w.EntryPrice, w.ATR14Pct)
		}
		w.Windows = append(w.Windows, x)
	}
	return w
}

func positiveFinite(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

func evaluateUpwardWindow(days []UpwardWaveDay, entry, atr float64) UpwardWaveWindow {
	full := upwardMetrics(days, entry, 1, len(days))
	peak := days[0].CloseNetPct
	for _, d := range days {
		peak = math.Max(peak, d.CloseNetPct)
	}
	giveback := peak - full.EndNetPct
	w := UpwardWaveWindow{Days: len(days), Status: "failed", Full: &full, PeakCloseNetPct: &peak, GivebackPct: &giveback}
	// Earliest completion, then earliest start, is deterministic. No hindsight
	// choice of the most profitable endpoint, and no re-anchoring to a trough.
	for end := 3; end <= len(days); end++ {
		for start := 1; start <= 2; start++ {
			if days[start-1].ChangePct <= 0 {
				continue
			}
			m := upwardMetrics(days, entry, start, end)
			if m.UpDays >= 3 && m.UpRatio >= .75 && m.LongestUp >= 2 && m.EndNetPct+1e-9 >= atr && m.MaxLossPct <= .5*atr+1e-9 && m.CloseDrawdownPct <= .5*atr+1e-9 {
				w.Status, w.Segment = "success", &m
				return w
			}
		}
	}
	return w
}

func upwardMetrics(days []UpwardWaveDay, entry float64, start, end int) UpwardWaveMetrics {
	m := UpwardWaveMetrics{StartDay: start, EndDay: end, Duration: end - start + 1, EndNetPct: days[end-1].CloseNetPct}
	peak, previous, distance, up, down := entry, entry, 0.0, 0, 0
	anchor := entry
	if start > 1 {
		anchor = days[start-2].Close
	}
	for i, d := range days[:end] {
		peak = math.Max(peak, d.Close)
		m.CloseDrawdownPct = math.Max(m.CloseDrawdownPct, (1-d.Close/peak)*100)
		m.MaxLossPct = math.Max(m.MaxLossPct, -d.LowNetPct)
		if i+1 >= start {
			distance += math.Abs(d.Close - previous)
			switch {
			case d.ChangePct > 0:
				m.UpDays++
				up++
				down = 0
			case d.ChangePct < 0:
				down++
				up = 0
			default:
				up, down = 0, 0
			}
			m.LongestUp, m.LongestDown = max(m.LongestUp, up), max(m.LongestDown, down)
		}
		previous = d.Close
	}
	m.UpRatio = float64(m.UpDays) / float64(m.Duration)
	if distance > 0 {
		m.Efficiency = (days[end-1].Close - anchor) / distance
	}
	return m
}

func summarizeUpwardWaves(trades []ExperimentTrade) *UpwardWaveSummary {
	summary := &UpwardWaveSummary{Model: UpwardWaveModel, Windows: []UpwardWaveStats{}}
	for index, n := range []int{5, 10} {
		s := UpwardWaveStats{Days: n, UnknownReasons: map[string]int{}}
		dates := map[string]bool{}
		var start, duration, efficiency, end, giveback, loss, worst float64
		for _, t := range trades {
			if !t.Selected {
				continue
			}
			s.Selected++
			dates[t.Date] = true
			w := t.UpwardWave
			if w == nil || w.Model != UpwardWaveModel || len(w.Windows) != 2 || w.EntryStatus == "unknown" {
				s.EntryUnknown++
				s.UnknownReasons["entry_evidence_missing"]++
				continue
			}
			if w.EntryStatus == "not_entered" {
				s.NotEntered++
				continue
			}
			s.Entered++
			x := w.Windows[index]
			if x.Status == "unknown" {
				s.Unknown++
				s.UnknownReasons[x.Reason]++
				continue
			}
			s.Known++
			if x.Status == "success" {
				s.Success++
				start += float64(x.Segment.StartDay)
				duration += float64(x.Segment.Duration)
				efficiency += x.Segment.Efficiency
			} else {
				s.Failed++
			}
			end += x.Full.EndNetPct
			giveback += *x.GivebackPct
			loss += x.Full.MaxLossPct
			worst = math.Max(worst, x.Full.MaxLossPct)
		}
		s.SignalDates = len(dates)
		s.Coverage, s.SuccessRate, s.ConservativeRate = waveRatio(float64(s.Known), s.Entered), waveRatio(float64(s.Success), s.Known), waveRatio(float64(s.Success), s.Entered)
		s.MeanStartDay, s.MeanDuration, s.MeanSegmentEfficiency = waveRatio(start, s.Success), waveRatio(duration, s.Success), waveRatio(efficiency, s.Success)
		s.MeanEndNetPct, s.MeanGivebackPct, s.MeanMaxLossPct = waveRatio(end, s.Known), waveRatio(giveback, s.Known), waveRatio(loss, s.Known)
		if s.Known > 0 {
			s.WorstMaxLossPct = &worst
		}
		summary.Windows = append(summary.Windows, s)
	}
	return summary
}

func waveRatio(value float64, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	x := value / float64(denominator)
	return &x
}
