package strategy

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	UpwardWaveModel           = "upward_wave_v0"
	UpwardWaveNoDrawdownModel = "upward_wave_no_drawdown_v1"
)

// UpwardWave is an ex-post path label, never an entry feature or an exit rule.
// Entry is the existing opening-price simulation's fill. Legacy exits do not
// truncate this path. All percentages use percentage points, ratios use [0,1].
type UpwardWave struct {
	Model              string                  `json:"model"`
	EntryStatus        string                  `json:"entry_status"` // entered / not_entered / unknown
	EntryReason        string                  `json:"entry_reason,omitempty"`
	EntryDate          string                  `json:"entry_date,omitempty"`
	EntryPrice         float64                 `json:"entry_price,omitempty"`
	ATR14Pct           float64                 `json:"signal_atr14_pct"`
	Days               []UpwardWaveDay         `json:"days"`
	Windows            []UpwardWaveWindow      `json:"windows"`
	PricePairReference *WavePricePairReference `json:"price_pair_reference,omitempty"`
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

// EvaluateOpeningUpwardWave observes an unranked stock/date using the same
// opening fill and wave definition as experiment trades. Histories must be
// sorted, unique and on a consistent price/source basis; missing sessions are
// allowed and remain unknown. Raw and adjusted histories need not have equal
// lengths. The only execution data read before labelling is the next opening
// session; no legacy stop/target result can filter the event.
func EvaluateOpeningUpwardWave(ctx context.Context, c config.Config, code, signalDate string, adjusted, raw []market.Bar, boundary, asof string) (*UpwardWave, error) {
	evaluator, err := NewOpeningWaveEvaluator(c)
	if err != nil {
		return nil, err
	}
	return evaluator.Evaluate(ctx, code, signalDate, adjusted, raw, boundary, asof)
}

// OpeningWaveEvaluator reuses the frozen calendar for a large event study.
// It is read-only after construction and retains no paths between calls.
type OpeningWaveEvaluator struct {
	parameters config.Strategy
	calendar   *calendar.Calendar
	model      string
}

func NewOpeningWaveEvaluator(c config.Config) (*OpeningWaveEvaluator, error) {
	return NewOpeningWaveEvaluatorForModel(c, UpwardWaveModel)
}

// NewOpeningWaveEvaluatorForModel selects an explicit, versioned path label.
// The legacy model retains its factor and risk checks. The no-drawdown model
// validates price pairs against a signal-prefix reference and reports risk
// measurements without using them as success gates.
func NewOpeningWaveEvaluatorForModel(c config.Config, model string) (*OpeningWaveEvaluator, error) {
	if !validUpwardWaveModel(model) {
		return nil, errors.New("unsupported upward-wave model")
	}
	if err := c.Strategy.Validate(); err != nil {
		return nil, err
	}
	if c.Daily.Adjust != "qfq" {
		return nil, errors.New("upward-wave features require qfq prices")
	}
	return &OpeningWaveEvaluator{parameters: c.Strategy, calendar: calendar.New(c), model: model}, nil
}

func validUpwardWaveModel(model string) bool {
	return model == UpwardWaveModel || model == UpwardWaveNoDrawdownModel
}

func (e *OpeningWaveEvaluator) Evaluate(ctx context.Context, code, signalDate string, adjusted, raw []market.Bar, boundary, asof string) (*UpwardWave, error) {
	w, _, err := e.evaluatePath(ctx, code, signalDate, adjusted, raw, boundary, asof, 10)
	return w, err
}

// evaluatePath establishes one opening fill and observes at most maxDays.
// Its returned stop reason belongs to the incomplete suffix only; completed
// five/ten-day windows retain their original independent classifications.
func (e *OpeningWaveEvaluator) evaluatePath(ctx context.Context, code, signalDate string, adjusted, raw []market.Bar, boundary, asof string, maxDays int) (*UpwardWave, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if e == nil || e.calendar == nil {
		return nil, "", errors.New("opening wave evaluator is unavailable")
	}
	var pairReference *WavePricePairReference
	unknown := func(reason string) (*UpwardWave, string, error) {
		w := unknownOpeningWaveForModel(reason, e.model)
		w.PricePairReference = pairReference
		return w, reason, nil
	}
	if !config.ValidCode(code) {
		return nil, "", errors.New("invalid stock code for upward-wave observation")
	}
	for _, date := range []string{signalDate, asof} {
		if parsed, err := time.Parse(time.DateOnly, date); err != nil || parsed.Format(time.DateOnly) != date {
			return nil, "", errors.New("upward-wave dates must be YYYY-MM-DD")
		}
	}
	if boundary != "" {
		if parsed, err := time.Parse(time.DateOnly, boundary); err != nil || parsed.Format(time.DateOnly) != boundary {
			return nil, "", errors.New("upward-wave boundary must be YYYY-MM-DD")
		}
	}
	if signalDate > asof || (boundary != "" && signalDate >= boundary) {
		return nil, "", errors.New("signal is outside the permitted observation interval")
	}
	cal := e.calendar
	if trading, err := cal.Trading(signalDate); err != nil || !trading {
		return nil, "", errors.New("signal date is outside the trading calendar")
	}
	ai := sort.Search(len(adjusted), func(i int) bool { return adjusted[i].Date >= signalDate })
	ri := sort.Search(len(raw), func(i int) bool { return raw[i].Date >= signalDate })
	validFeatureBar := validWaveBar
	if e.model == UpwardWaveNoDrawdownModel {
		validFeatureBar = validWavePriceBar
	}
	if ai >= len(adjusted) || adjusted[ai].Date != signalDate || ai < 59 || !validFeatureBar(adjusted[ai]) || ri >= len(raw) || raw[ri].Date != signalDate || !validFeatureBar(raw[ri]) {
		return unknown("signal_data_missing")
	}
	for _, bar := range adjusted[ai-14 : ai+1] {
		if !validFeatureBar(bar) {
			return unknown("atr_unknown")
		}
	}
	atr := signalATR14Pct(adjusted[:ai+1])
	if e.model == UpwardWaveNoDrawdownModel {
		reference, reason := NewWavePricePairReference(adjusted[:ai+1], raw[:ri+1])
		if reason != "" {
			return unknown(reason)
		}
		pairReference = &reference
	}
	next, err := cal.Next(signalDate)
	if err != nil {
		return unknown("calendar_missing")
	}
	if boundary != "" && next >= boundary {
		return unknown("reserved_boundary")
	}
	if next > asof {
		return unknown("future_window_incomplete")
	}
	if ri+1 >= len(raw) || raw[ri+1].Date != next {
		return unknown("session_missing")
	}
	if !validWaveBar(raw[ri+1]) {
		return unknown("bar_invalid_or_suspended")
	}
	// Passing only D1 lets the existing simulator establish its actual opening
	// fill and cost, then stop with an unresolved legacy exit. No D2 price is
	// consulted; the full future path is evaluated independently below.
	outcome, err := Simulate(e.parameters, cal, code, raw[ri], raw[ri+1:ri+2], 1)
	if err != nil {
		return nil, "", err
	}
	t := ExperimentTrade{Code: code, Date: signalDate, Selected: true, Features: Features{ATR14Pct: atr}, Outcome: outcome}
	d := Dataset{Adjusted: market.History{Bars: adjusted}, Raw: market.History{Bars: raw}}
	return labelUpwardWavePath(ctx, e.parameters, cal, t, d, boundary, asof, e.model, pairReference, maxDays)
}

func unknownOpeningWave(reason string) *UpwardWave {
	return unknownOpeningWaveForModel(reason, UpwardWaveModel)
}

func unknownOpeningWaveForModel(reason, model string) *UpwardWave {
	w := &UpwardWave{Model: model, EntryStatus: "unknown", EntryReason: reason, Days: []UpwardWaveDay{}, Windows: []UpwardWaveWindow{}}
	for _, days := range []int{5, 10} {
		w.Windows = append(w.Windows, UpwardWaveWindow{Days: days, Status: "unknown", Reason: reason})
	}
	return w
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
	return labelUpwardWaveForModel(s, cal, t, d, boundary, asof, UpwardWaveModel, nil)
}

func labelUpwardWaveForModel(s config.Strategy, cal *calendar.Calendar, t ExperimentTrade, d Dataset, boundary, asof, model string, pairReference *WavePricePairReference) *UpwardWave {
	w, _, _ := labelUpwardWavePath(context.Background(), s, cal, t, d, boundary, asof, model, pairReference, 10)
	return w
}

func labelUpwardWavePath(ctx context.Context, s config.Strategy, cal *calendar.Calendar, t ExperimentTrade, d Dataset, boundary, asof, model string, pairReference *WavePricePairReference, maxDays int) (*UpwardWave, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	w := &UpwardWave{Model: model, EntryStatus: "unknown", ATR14Pct: t.Features.ATR14Pct, Days: []UpwardWaveDay{}, Windows: []UpwardWaveWindow{}, PricePairReference: pairReference}
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
		return w, w.EntryReason, nil
	}
	w.EntryStatus, w.EntryDate, w.EntryPrice = "entered", t.Outcome.EntryDate, entry.Fill
	p := Plan{EntryPrice: entry.Fill, BuyCost: entry.BuyCost, Shares: entry.Shares}
	raw, adj := d.Raw.Bars, d.Adjusted.Bars
	i := sort.Search(len(raw), func(i int) bool { return raw[i].Date >= t.Date })
	ai := sort.Search(len(adj), func(i int) bool { return adj[i].Date >= t.Date })
	reason := ""
	if i >= len(raw) || ai >= len(adj) || raw[i].Date != t.Date || adj[ai].Date != t.Date || !positiveFinite(raw[i].Close) || !positiveFinite(adj[ai].Close) {
		reason = "signal_data_missing"
	} else if !positiveFinite(w.ATR14Pct) {
		reason = "atr_unknown"
	} else {
		factor, previous, date := adj[ai].Close/raw[i].Close, entry.Fill, t.Date
		for day := 1; day <= maxDays; day++ {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
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
			j := sort.Search(len(raw), func(j int) bool { return raw[j].Date >= expected })
			aj := sort.Search(len(adj), func(j int) bool { return adj[j].Date >= expected })
			if j >= len(raw) || aj >= len(adj) || raw[j].Date != expected || adj[aj].Date != expected {
				reason = "session_missing"
				break
			}
			b, a := raw[j], adj[aj]
			if !validWaveBar(b) || !validWaveBar(a) || b.Volume <= 0 || a.Volume <= 0 {
				reason = "bar_invalid_or_suspended"
				break
			}
			if day == 1 && expected != w.EntryDate {
				reason = "entry_date_mismatch"
				break
			}
			if model == UpwardWaveNoDrawdownModel {
				if pairReference == nil {
					reason = "price_pair_reference_missing"
					break
				}
				if reason = pairReference.Validate(a, b); reason != "" {
					break
				}
			} else if math.Abs((a.Close/b.Close)/factor-1) > .002 {
				// The legacy model keeps its historical factor tolerance and label.
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
			x = evaluateUpwardWindowForModel(w.Days[:n], w.EntryPrice, w.ATR14Pct, model)
		}
		w.Windows = append(w.Windows, x)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return w, reason, nil
}

func positiveFinite(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

// Normalized volume is pairing evidence, not an ATR input. In the new model,
// only the bounded pairing prefix should reject unusable historical shares.
func validWavePriceBar(bar market.Bar) bool {
	bar.Volume = 0
	return validWaveBar(bar)
}

func evaluateUpwardWindow(days []UpwardWaveDay, entry, atr float64) UpwardWaveWindow {
	return evaluateUpwardWindowForModel(days, entry, atr, UpwardWaveModel)
}

func evaluateUpwardWindowForModel(days []UpwardWaveDay, entry, atr float64, model string) UpwardWaveWindow {
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
			riskPassed := model == UpwardWaveNoDrawdownModel || (m.MaxLossPct <= .5*atr+1e-9 && m.CloseDrawdownPct <= .5*atr+1e-9)
			if m.UpDays >= 3 && m.UpRatio >= .75 && m.LongestUp >= 2 && m.EndNetPct+1e-9 >= atr && riskPassed {
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
	var accumulator UpwardWaveAccumulator
	for _, trade := range trades {
		if trade.Selected {
			accumulator.Add(trade.Date, trade.UpwardWave)
		}
	}
	return accumulator.Summary()
}

func waveRatio(value float64, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	x := value / float64(denominator)
	return &x
}
