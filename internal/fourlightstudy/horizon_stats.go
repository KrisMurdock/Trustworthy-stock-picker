package fourlightstudy

import (
	"errors"
	"sort"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

var horizonContextGroups = []string{stockOutperformSector, stockNotOutperformSector, sectorOutperformMarket, sectorNotOutperformMarket, contextUnknown}
var horizonGroups = append([]string{"baseline", "first_all_red", "episode_first", "episode_repeat", "episode_unknown", moderateVolumeGroup}, horizonContextGroups...)
var horizonLengths = []int{20, 40, 60}
var horizonStartupStates = []string{"all", "success", "failed", "unknown", "not_entered"}

// HorizonStats describes complete paths, never a long-window success label.
// StartupFive other than all is an ex-post D5 stratum, not a signal feature.
type HorizonStats struct {
	Name                        string         `json:"name"`
	Date                        string         `json:"date,omitempty"`
	Month                       string         `json:"month,omitempty"`
	StartupFive                 string         `json:"startup_five"`
	Days                        int            `json:"days"`
	Selected                    int            `json:"selected"`
	Entered                     int            `json:"entered"`
	NotEntered                  int            `json:"not_entered"`
	EntryUnknown                int            `json:"entry_unknown"`
	Known                       int            `json:"known"`
	Unknown                     int            `json:"unknown"`
	UnknownReasons              map[string]int `json:"unknown_reasons"`
	KnownSignalDates            int            `json:"known_signal_dates"`
	Coverage                    *float64       `json:"coverage"`
	EffectiveCoverage           *float64       `json:"effective_coverage"`
	MeanEndNetPct               *float64       `json:"mean_end_net_pct"`
	MeanPeakCloseNetPct         *float64       `json:"mean_peak_close_net_pct"`
	MeanGivebackPct             *float64       `json:"mean_giveback_pct"`
	MeanUpRatio                 *float64       `json:"mean_up_ratio"`
	MeanLongestUp               *float64       `json:"mean_longest_up"`
	MeanEfficiency              *float64       `json:"mean_efficiency"`
	MeanMaxLossPct              *float64       `json:"mean_max_loss_pct"`
	MeanCloseDrawdownPct        *float64       `json:"mean_close_drawdown_pct"`
	MeanAfterDay5CloseReturnPct *float64       `json:"mean_after_day5_close_return_pct"`
	MeanAfterDay5UpRatio        *float64       `json:"mean_after_day5_up_ratio"`
	MeanAfterDay5LongestUp      *float64       `json:"mean_after_day5_longest_up"`
	MeanNewClosingHighDays      *float64       `json:"mean_new_closing_high_days"`
}

type horizonKey struct {
	period, name, startup string
	days                  int
}
type horizonAccumulator struct {
	stats HorizonStats
	sums  [12]float64
	dates map[string]bool
}
type horizonGrid map[horizonKey]*horizonAccumulator

func horizonValues(w strategy.WaveHorizonWindow) [12]float64 {
	return [12]float64{w.Full.EndNetPct, *w.PeakCloseNetPct, *w.GivebackPct, w.Full.UpRatio, float64(w.Full.LongestUp), w.Full.Efficiency, w.Full.MaxLossPct, w.Full.CloseDrawdownPct, *w.AfterDay5CloseReturnPct, *w.AfterDay5UpRatio, float64(*w.AfterDay5LongestUp), float64(*w.NewClosingHighDays)}
}
func horizonRatio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
func (g horizonGrid) cell(key horizonKey) *horizonAccumulator {
	if g[key] == nil {
		g[key] = &horizonAccumulator{stats: HorizonStats{Name: key.name, StartupFive: key.startup, Days: key.days, UnknownReasons: map[string]int{}}, dates: map[string]bool{}}
	}
	return g[key]
}
func (g horizonGrid) add(period, date string, names []string, h *strategy.WaveHorizons) {
	for _, name := range names {
		for _, startup := range []string{"all", h.StartupFive.Status} {
			for _, w := range h.Windows {
				a := g.cell(horizonKey{period, name, startup, w.Days})
				a.stats.Selected++
				switch h.EntryStatus {
				case "not_entered":
					a.stats.NotEntered++
				case "unknown":
					a.stats.EntryUnknown++
					a.stats.UnknownReasons[h.EntryReason]++
				case "entered":
					a.stats.Entered++
					if w.Status != "known" {
						a.stats.Unknown++
						a.stats.UnknownReasons[w.Reason]++
						continue
					}
					a.stats.Known++
					a.dates[date] = true
					for i, v := range horizonValues(w) {
						a.sums[i] += v
					}
				}
			}
		}
	}
}
func (g horizonGrid) rows(period, kind string) []HorizonStats {
	out := []HorizonStats{}
	for _, name := range horizonGroups {
		for _, startup := range horizonStartupStates {
			for _, days := range horizonLengths {
				a := g.cell(horizonKey{period, name, startup, days})
				s := a.stats
				if kind == "daily" {
					s.Date = period
				}
				if kind == "monthly" {
					s.Month = period
				}
				s.KnownSignalDates = len(a.dates)
				s.Coverage = horizonRatio(s.Known, s.Entered)
				s.EffectiveCoverage = horizonRatio(s.Known, s.Selected)
				fields := []**float64{&s.MeanEndNetPct, &s.MeanPeakCloseNetPct, &s.MeanGivebackPct, &s.MeanUpRatio, &s.MeanLongestUp, &s.MeanEfficiency, &s.MeanMaxLossPct, &s.MeanCloseDrawdownPct, &s.MeanAfterDay5CloseReturnPct, &s.MeanAfterDay5UpRatio, &s.MeanAfterDay5LongestUp, &s.MeanNewClosingHighDays}
				if s.Known > 0 {
					for i, field := range fields {
						v := a.sums[i] / float64(s.Known)
						*field = &v
					}
				}
				out = append(out, s)
			}
		}
	}
	return out
}
func validateHorizonEvent(e Event) error {
	h := e.Horizons
	if h == nil || h.Model != strategy.WaveHorizonsModel || len(h.Windows) != 3 {
		return errors.New("missing horizon evidence")
	}
	if h.EntryStatus != "entered" && h.EntryStatus != "not_entered" && h.EntryStatus != "unknown" {
		return errors.New("invalid horizon entry state")
	}
	if h.StartupFive.Status != "success" && h.StartupFive.Status != "failed" && h.StartupFive.Status != "unknown" && h.StartupFive.Status != "not_entered" {
		return errors.New("invalid startup stratum")
	}
	for i, w := range h.Windows {
		if w.Days != horizonLengths[i] {
			return errors.New("invalid horizon length")
		}
		if w.Status == "known" && (h.EntryStatus != "entered" || w.Full == nil || w.PeakCloseNetPct == nil || w.GivebackPct == nil || w.AfterDay5CloseReturnPct == nil || w.AfterDay5UpRatio == nil || w.AfterDay5LongestUp == nil || w.NewClosingHighDays == nil) {
			return errors.New("incomplete known horizon")
		}
	}
	return nil
}
func horizonNamesForEvent(e Event) ([]string, error) {
	names := []string{"baseline"}
	if !hasSectorGroup(e.Groups, "first_all_red") {
		return names, nil
	}
	if e.Episode == nil {
		return nil, errors.New("missing horizon episode evidence")
	}
	switch e.Episode.Group {
	case "first", "repeat", "unknown":
	default:
		return nil, errors.New("invalid horizon episode")
	}
	names = append(names, "first_all_red", "episode_"+e.Episode.Group)
	if hasSectorGroup(e.Groups, moderateVolumeGroup) {
		names = append(names, moderateVolumeGroup)
	}
	if c := e.Context; c != nil && c.Policy == SectorContextPolicy {
		switch c.StockStage {
		case "outperform":
			names = append(names, stockOutperformSector)
		case "not_outperform":
			names = append(names, stockNotOutperformSector)
		}
		switch c.SectorStage {
		case "outperform":
			names = append(names, sectorOutperformMarket)
		case "not_outperform":
			names = append(names, sectorNotOutperformMarket)
		}
		if c.StockStage == "unknown" || c.SectorStage == "unknown" {
			names = append(names, contextUnknown)
		}
	}
	return names, nil
}

type horizonMatchKey struct {
	date, sector, name string
	days               int
}
type horizonMatchCell struct {
	selected, known int
	sums            [3]float64
}
type horizonMatchGrid map[horizonMatchKey]*horizonMatchCell

func (g horizonMatchGrid) add(e Event, names []string) {
	for _, w := range e.Horizons.Windows {
		for _, name := range names {
			key := horizonMatchKey{e.Date, e.Context.SectorCode, name, w.Days}
			a := g[key]
			if a == nil {
				a = &horizonMatchCell{}
				g[key] = a
			}
			a.selected++
			if w.Status == "known" {
				a.known++
				for i, v := range [3]float64{w.Full.EndNetPct, *w.AfterDay5CloseReturnPct, *w.AfterDay5UpRatio} {
					a.sums[i] += v
				}
			}
		}
	}
}

// Continuous outcomes are compared in their own units: percentage points for
// return fields, ratio differences for up_ratio. No ten-day bootstrap is reused
// for these highly overlapping 20/40/60-session outcomes.
type HorizonComparison struct {
	Group          string   `json:"group"`
	Reference      string   `json:"reference"`
	Metric         string   `json:"metric"`
	Unit           string   `json:"unit"`
	Days           int      `json:"days"`
	Month          string   `json:"month,omitempty"`
	Selected       int      `json:"selected"`
	CandidateKnown int      `json:"candidate_known"`
	MatchedKnown   int      `json:"matched_known"`
	UnmatchedKnown int      `json:"unmatched_known"`
	PairedDates    int      `json:"paired_dates"`
	UnpairedDates  int      `json:"unpaired_dates"`
	MeanDifference *float64 `json:"mean_difference"`
	Status         string   `json:"status"`
}

func (g horizonMatchGrid) compare(dates []string, month string, daily horizonGrid) []HorizonComparison {
	out := []HorizonComparison{}
	metrics := []string{"end_net_pct", "after_day5_close_return_pct", "after_day5_up_ratio"}
	sectors := map[string]bool{}
	for key := range g {
		sectors[key.sector] = true
	}
	sectorNames := make([]string, 0, len(sectors))
	for s := range sectors {
		sectorNames = append(sectorNames, s)
	}
	sort.Strings(sectorNames)
	for _, name := range horizonGroups[1:] {
		for _, ref := range []string{"baseline", "matched_sector", "episode_first"} {
			if ref == "episode_first" && name != "episode_repeat" {
				continue
			}
			for _, days := range horizonLengths {
				for mi, metric := range metrics {
					row := HorizonComparison{Group: name, Reference: ref, Metric: metric, Unit: "percentage_points", Days: days, Month: month, Status: "no_paired_dates"}
					if mi == 2 {
						row.Unit = "ratio"
					}
					sum := 0.0
					for _, date := range dates {
						a := daily.cell(horizonKey{date, name, "all", days})
						row.Selected += a.stats.Selected
						row.CandidateKnown += a.stats.Known
						if ref != "matched_sector" {
							b := daily.cell(horizonKey{date, ref, "all", days})
							if a.stats.Known == 0 || b.stats.Known == 0 {
								row.UnpairedDates++
								continue
							}
							idx := []int{0, 8, 9}[mi]
							sum += a.sums[idx]/float64(a.stats.Known) - b.sums[idx]/float64(b.stats.Known)
							row.MatchedKnown += a.stats.Known
							row.PairedDates++
						} else {
							dateSum, count := 0.0, 0
							for _, sector := range sectorNames {
								candidate := g[horizonMatchKey{date, sector, name, days}]
								base := g[horizonMatchKey{date, sector, "baseline", days}]
								if sector == "" || candidate == nil || candidate.known == 0 || base == nil || base.known < sectorMinimumOthers+1 {
									continue
								}
								// Sum per-candidate (x - (baseline_sum-x)/(baseline_n-1)).
								dateSum += (float64(base.known)*candidate.sums[mi] - float64(candidate.known)*base.sums[mi]) / float64(base.known-1)
								count += candidate.known
							}
							if count == 0 {
								row.UnpairedDates++
								continue
							}
							sum += dateSum / float64(count)
							row.MatchedKnown += count
							row.PairedDates++
						}
					}
					row.UnmatchedKnown = row.CandidateKnown - row.MatchedKnown
					if row.PairedDates > 0 {
						v := sum / float64(row.PairedDates)
						row.MeanDifference = &v
						row.Status = "descriptive_only_no_interval"
					}
					out = append(out, row)
				}
			}
		}
	}
	return out
}
