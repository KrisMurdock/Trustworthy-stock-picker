package strategy

import (
	"errors"
	"math"
)

// UpwardWaveAccumulator is a bounded-memory, event-weighted summary shared by
// ranked experiments and unranked market studies. The zero value is ready to
// use; Add retains no wave paths. Each input is one selected stock/date event.
type UpwardWaveAccumulator struct {
	windows [2]upwardWindowAccumulator
	model   string
}

// NewUpwardWaveAccumulator explicitly selects a model. A zero-value
// accumulator continues to summarize only the legacy upward_wave_v0 model.
func NewUpwardWaveAccumulator(model string) (*UpwardWaveAccumulator, error) {
	if !validUpwardWaveModel(model) {
		return nil, errors.New("unsupported upward-wave model")
	}
	return &UpwardWaveAccumulator{model: model}, nil
}

func (a *UpwardWaveAccumulator) modelName() string {
	if a.model == "" {
		return UpwardWaveModel
	}
	return a.model
}

type upwardWindowAccumulator struct {
	stats                                      UpwardWaveStats
	dates                                      map[string]bool
	start, duration, efficiency, end, giveback float64
	loss, worst                                float64
}

func (a *UpwardWaveAccumulator) Add(date string, wave *UpwardWave) {
	for index, days := range []int{5, 10} {
		acc := &a.windows[index]
		s := &acc.stats
		if acc.dates == nil {
			acc.dates = map[string]bool{}
			s.Days = days
			s.UnknownReasons = map[string]int{}
		}
		s.Selected++
		acc.dates[date] = true
		if wave == nil || wave.Model != a.modelName() || len(wave.Windows) != 2 || wave.EntryStatus == "unknown" {
			s.EntryUnknown++
			reason := "entry_evidence_missing"
			if wave != nil && wave.EntryReason != "" {
				reason = wave.EntryReason
			}
			if wave != nil && wave.Model != a.modelName() {
				reason = "wave_model_mismatch"
			}
			s.UnknownReasons[reason]++
			continue
		}
		if wave.EntryStatus == "not_entered" {
			s.NotEntered++
			continue
		}
		s.Entered++
		w := wave.Windows[index]
		if w.Status == "unknown" {
			s.Unknown++
			s.UnknownReasons[w.Reason]++
			continue
		}
		if w.Days != days || w.Full == nil || w.GivebackPct == nil || (w.Status != "success" && w.Status != "failed") || (w.Status == "success" && w.Segment == nil) {
			s.Unknown++
			s.UnknownReasons["invalid_wave_evidence"]++
			continue
		}
		s.Known++
		if w.Status == "success" {
			s.Success++
			acc.start += float64(w.Segment.StartDay)
			acc.duration += float64(w.Segment.Duration)
			acc.efficiency += w.Segment.Efficiency
		} else {
			s.Failed++
		}
		acc.end += w.Full.EndNetPct
		acc.giveback += *w.GivebackPct
		acc.loss += w.Full.MaxLossPct
		acc.worst = math.Max(acc.worst, w.Full.MaxLossPct)
	}
}

// Summary returns a detached value, so later Add calls cannot revise an
// already archived summary. Empty denominators stay nil rather than zero.
func (a *UpwardWaveAccumulator) Summary() *UpwardWaveSummary {
	summary := &UpwardWaveSummary{Model: a.modelName(), Windows: []UpwardWaveStats{}}
	for index, days := range []int{5, 10} {
		acc := a.windows[index]
		s := acc.stats
		s.Days = days
		s.UnknownReasons = map[string]int{}
		for reason, count := range acc.stats.UnknownReasons {
			s.UnknownReasons[reason] = count
		}
		s.SignalDates = len(acc.dates)
		s.Coverage = waveRatio(float64(s.Known), s.Entered)
		s.SuccessRate = waveRatio(float64(s.Success), s.Known)
		s.ConservativeRate = waveRatio(float64(s.Success), s.Entered)
		s.MeanStartDay = waveRatio(acc.start, s.Success)
		s.MeanDuration = waveRatio(acc.duration, s.Success)
		s.MeanSegmentEfficiency = waveRatio(acc.efficiency, s.Success)
		s.MeanEndNetPct = waveRatio(acc.end, s.Known)
		s.MeanGivebackPct = waveRatio(acc.giveback, s.Known)
		s.MeanMaxLossPct = waveRatio(acc.loss, s.Known)
		if s.Known > 0 {
			worst := acc.worst
			s.WorstMaxLossPct = &worst
		}
		summary.Windows = append(summary.Windows, s)
	}
	return summary
}
