package fourlightstudy

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPriceEpisodeStudyPreservesReportsAndPartitions(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	var before, after []Event
	base, sector, trigger, err := AnalyzeTriggerTiming(context.Background(), m, source, sectors, func(e Event) error { before = append(before, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	got, gotSector, gotTrigger, r, err := AnalyzePriceEpisodeStudy(context.Background(), m, source, sectors, func(e Event) error { after = append(after, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base, got) || !reflect.DeepEqual(sector, gotSector) || !reflect.DeepEqual(trigger, gotTrigger) {
		t.Fatal("episode study changed prior reports")
	}
	if r.Status != "complete" || r.Policy != PriceEpisodePolicy || len(before) != len(after) || r.Audit.AllRedEvents != base.Audit.AllRedEventRows {
		t.Fatal("incomplete episode output")
	}
	for i, e := range after {
		if e.Episode == nil || e.Episode.AsOf != e.Date {
			t.Fatal("missing current episode")
		}
		e.Episode = nil
		if !reflect.DeepEqual(e, before[i]) {
			t.Fatal("old event changed")
		}
	}
	for _, days := range []int{5, 10} {
		for _, partition := range []struct {
			base  string
			names []string
		}{{"first_all_red", episodePartition}, {"first_price_age_6_plus", episodeAgeSixPartition}} {
			original := groupWindow(t, Report{Groups: trigger.Groups}, partition.base, days)
			count := [7]int{}
			for _, name := range partition.names {
				w := groupWindow(t, Report{Groups: r.Groups}, name, days)
				v := [7]int{w.Selected, w.Entered, w.EntryUnknown, w.NotEntered, w.Known, w.Unknown, w.Success}
				for i := range count {
					count[i] += v[i]
				}
			}
			if count != [7]int{original.Selected, original.Entered, original.EntryUnknown, original.NotEntered, original.Known, original.Unknown, original.Success} {
				t.Fatalf("lost denominator %s: %v", partition.base, count)
			}
		}
	}
	if r.Audit.FirstAllRed != r.Audit.EpisodeFirst+r.Audit.EpisodeRepeat+r.Audit.EpisodeUnknown {
		t.Fatal("lost original first events")
	}
	if len(r.EpisodeFirstComparisons) != 2 || len(r.MonthlyComparisons) != 1 {
		t.Fatal("missing repeated-vs-first comparison")
	}
}

func TestPriceEpisodeFutureAndStudyStartDoNotSelectCandidates(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	var before, after []Event
	_, _, _, old, err := AnalyzePriceEpisodeStudy(context.Background(), m, source, sectors, func(e Event) error { before = append(before, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.From = m.Through
	for code, pair := range source.pairs {
		pair.Raw = pair.Raw[:81]
		pair.RawShares = pair.RawShares[:81]
		source.pairs[code] = pair
	}
	_, _, _, now, err := AnalyzePriceEpisodeStudy(context.Background(), m, source, sectors, func(e Event) error { after = append(after, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	prior := map[string]Event{}
	for _, e := range before {
		prior[e.Code+e.Date] = e
	}
	for _, e := range after {
		if !reflect.DeepEqual(e.Episode, prior[e.Code+e.Date].Episode) {
			t.Fatal("research start or future coverage changed episode")
		}
	}
	if len(after) == 0 || old.Audit.FirstAllRed == 0 {
		t.Fatal("empty fixture")
	}
	if groupWindow(t, Report{Groups: now.Groups}, "first_all_red", 5).Known != 0 {
		t.Fatal("fixture did not remove known future")
	}
}

func TestPriceEpisodeStudyErrorsReturnNoPartialReports(t *testing.T) {
	for _, mode := range []string{"source", "sink", "cancel", "legacy", "classification", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			m, source, sectors := sectorStudyFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sink func(Event) error
			switch mode {
			case "source":
				source.loadErr = errors.New("source failure")
			case "sink":
				sink = func(Event) error { return errors.New("sink failure") }
			case "cancel":
				cancel()
			case "legacy":
				m.Version = 1
			case "classification":
				sectors = SectorSnapshot{}
			case "timeout":
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, -1)
				defer stop()
			}
			base, sector, trigger, r, err := AnalyzePriceEpisodeStudy(ctx, m, source, sectors, sink)
			if err == nil || sector != nil || trigger != nil || r != nil || !reflect.DeepEqual(base, Report{}) {
				t.Fatal("partial successful result on failure")
			}
		})
	}
}

func TestPriceEpisodePrecomputationDoesNotLeakLaterAdjustedPrices(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	var before, after []Event
	_, _, _, _, err := AnalyzePriceEpisodeStudy(context.Background(), m, source, sectors, func(e Event) error {
		if e.Date == m.From {
			before = append(before, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for code, pair := range source.pairs {
		for i := range pair.Adjusted {
			if pair.Adjusted[i].Date > m.From {
				pair.Adjusted[i].Open, pair.Adjusted[i].High = 1, 1
				pair.Adjusted[i].Low, pair.Adjusted[i].Close, pair.Adjusted[i].Volume = 1, 1, 0
			}
		}
		source.pairs[code] = pair
	}
	_, _, _, _, err = AnalyzePriceEpisodeStudy(context.Background(), m, source, sectors, func(e Event) error {
		if e.Date == m.From {
			after = append(after, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 || len(before) != len(after) {
		t.Fatal("lost early events")
	}
	for i := range before {
		if !reflect.DeepEqual(before[i].Episode, after[i].Episode) {
			t.Fatal("later adjusted history changed an earlier episode")
		}
	}
}

func TestEpisodeGroupsDependOnPastEvidenceOnly(t *testing.T) {
	for _, group := range []string{"first", "repeat", "unknown"} {
		e := Event{Groups: []string{"baseline", "first_all_red"}, Episode: &PriceEpisodeContext{Group: group}, Trigger: &TriggerContext{PriceAgeGroup: "6+"}}
		got, err := episodeNamesForEvent(e)
		want := []string{"baseline", "first_all_red", "episode_" + group, "first_price_age_6_plus", "age6_episode_" + group}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("lost unknown/no-future group %v %v", got, err)
		}
	}
	for _, e := range []Event{{Groups: []string{"first_all_red"}}, {Groups: []string{"first_all_red"}, Episode: &PriceEpisodeContext{Group: "not_applicable"}, Trigger: &TriggerContext{}}} {
		if _, err := episodeNamesForEvent(e); err == nil {
			t.Fatal("accepted invalid first episode")
		}
	}
}
