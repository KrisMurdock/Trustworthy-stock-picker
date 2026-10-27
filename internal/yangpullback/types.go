// Package yangpullback implements the frozen Phase 0 research definition for
// an uptrend pullback that holds the last bullish candle. It does not create
// production recommendations.
package yangpullback

const (
	Version = 1
	Rule    = "yang_pullback_v1"
)

// Mode selects the frozen pattern or one of its matched controls. Controls use
// the same trend and pullback timing so the pattern increment can be isolated.
type Mode string

const (
	ModePattern      Mode = "pattern"
	ModePullbackOnly Mode = "pullback_only"
	ModeHoldOnly     Mode = "hold_only"
)

// Options freezes every lookback and eligibility parameter used by the study.
type Options struct {
	AsOf         string
	HighWindow   int
	PullbackMin  int
	PullbackMax  int
	PhaseWindow  int
	RequirePhase bool
}

func DefaultOptions() Options {
	return Options{
		HighWindow:   10,
		PullbackMin:  2,
		PullbackMax:  5,
		PhaseWindow:  240,
		RequirePhase: true,
	}
}

func (o Options) Normalize() (Options, error) {
	if o.HighWindow == 0 {
		o.HighWindow = 10
	}
	if o.PullbackMin == 0 {
		o.PullbackMin = 2
	}
	if o.PullbackMax == 0 {
		o.PullbackMax = 5
	}
	if o.PhaseWindow == 0 {
		o.PhaseWindow = 240
	}
	if o.HighWindow < 3 || o.HighWindow > 120 {
		return o, errInvalid("high window")
	}
	if o.PullbackMin < 1 || o.PullbackMax < o.PullbackMin || o.PullbackMax > 20 {
		return o, errInvalid("pullback window")
	}
	if o.PhaseWindow < 60 || o.PhaseWindow > 1000 {
		return o, errInvalid("phase window")
	}
	return o, nil
}

type Signal struct {
	Version         int     `json:"schema_version"`
	Rule            string  `json:"rule"`
	Code            string  `json:"code"`
	Date            string  `json:"date"`
	HighDate        string  `json:"high_date"`
	High            float64 `json:"high"`
	AnchorDate      string  `json:"anchor_date"`
	AnchorLow       float64 `json:"anchor_low"`
	PullbackDays    int     `json:"pullback_days"`
	Phase           string  `json:"phase"`
	PhaseConfidence string  `json:"phase_confidence"`
	PhaseScore      float64 `json:"phase_confidence_score"`
}

// Trade records one signal and its fixed-horizon execution. Resolved is false
// when the entry or exit could not be established from completed raw bars.
type Trade struct {
	Signal       Signal  `json:"signal"`
	EntryDate    string  `json:"entry_date,omitempty"`
	ExitDate     string  `json:"exit_date,omitempty"`
	NetPct       float64 `json:"net_pct"`
	HoldingDays  int     `json:"holding_days"`
	Resolved     bool    `json:"resolved"`
	EntrySkipped bool    `json:"entry_skipped"`
	Status       string  `json:"status"`
	MFE          float64 `json:"mfe_pct"`
	MAE          float64 `json:"mae_pct"`
	MaxHighPct   float64 `json:"max_high_pct"`
	PriceTouch   bool    `json:"price_touch"`
	TargetFirst  bool    `json:"target_first"`
	StopFirst    bool    `json:"stop_first"`
	Ambiguous    bool    `json:"ambiguous"`
}

type invalidError string

func (e invalidError) Error() string { return "invalid yang-pullback option: " + string(e) }

func errInvalid(field string) error { return invalidError(field) }
