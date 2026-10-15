package strategy

import "math"

// HighTouch records a daily-high opportunity, independently of the simulated
// execution. Versioned observations distinguish missing legacy data from a miss.
type HighTouch struct {
	Version      int     `json:"schema_version"`
	ObservedDays int     `json:"observed_days"`
	MaxNetPct    float64 `json:"max_net_pct"`
	Day25        int     `json:"first_2_5_day"`
	Day30        int     `json:"first_3_day"`
	Day25Unknown bool    `json:"first_2_5_order_unknown"`
	Day30Unknown bool    `json:"first_3_order_unknown"`
	Ambiguous    bool    `json:"exit_day_order_unknown"`
}

func (h *HighTouch) observe(day int, net float64) {
	if h.ObservedDays == 0 {
		h.MaxNetPct = net
	} else {
		h.MaxNetPct = math.Max(h.MaxNetPct, net)
	}
	h.ObservedDays++
	if h.Day25 == 0 && net >= 2.5 {
		h.Day25 = day
	}
	if h.Day30 == 0 && net >= 3 {
		h.Day30 = day
	}
}
