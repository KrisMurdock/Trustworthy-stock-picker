package experiment_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestHoldoutLifecyclePreservesParentAndMarksRepeatedConsumption(t *testing.T) {
	ctx := context.Background()
	snapshot, parent := holdoutLifecycleFixture(t)
	if len(snapshot.Calendar.Days) == 0 || snapshot.Calendar.Digest == "" {
		t.Fatal("fixture did not freeze a trading calendar with a digest")
	}

	db, err := storage.OpenExperiments(ctx, t.TempDir(), false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err = db.Start(ctx, snapshot, parent); err != nil {
		t.Fatal(err)
	}
	if err = experiment.Evaluate(ctx, snapshot, &parent, nil); err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(ctx, parent); err != nil {
		t.Fatal(err)
	}
	original, err := db.Get(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if original.Result == nil || original.Result.Development.HoldoutFrom == "" || original.Result.Development.HoldoutThrough == "" {
		t.Fatal("parent has no reserved holdout interval")
	}
	parentBefore, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	consumed, err := db.HoldoutConsumed(ctx, snapshot.ID)
	if err != nil || consumed {
		t.Fatalf("first consumption was pre-marked: consumed=%v err=%v", consumed, err)
	}
	first, err := experiment.NewHoldoutRecord(snapshot, original, consumed)
	if err != nil {
		t.Fatal(err)
	}
	if first.Holdout == nil || first.Holdout.Contaminated {
		t.Fatalf("first holdout child is contaminated: %+v", first.Holdout)
	}
	if first.Holdout.From != original.Result.Development.HoldoutFrom || first.Holdout.Through != original.Result.Development.HoldoutThrough {
		t.Fatal("holdout child changed the parent's reserved interval")
	}
	tamperedRange, err := experiment.NewHoldoutRecord(snapshot, original, consumed)
	if err != nil {
		t.Fatal(err)
	}
	tamperedRange.Holdout.From = snapshot.Adjusted[0].Bars[0].Date
	if err = db.Start(ctx, snapshot, tamperedRange); err == nil {
		t.Fatal("storage accepted a holdout outside the parent reservation")
	}
	tamperedSpec, err := experiment.NewHoldoutRecord(snapshot, original, consumed)
	if err != nil {
		t.Fatal(err)
	}
	tamperedSpec.Spec.Title = "changed after development"
	if err = db.Start(ctx, snapshot, tamperedSpec); err == nil {
		t.Fatal("storage accepted a holdout with a changed rule")
	}
	if err = db.Start(ctx, snapshot, first); err != nil {
		t.Fatal(err)
	}
	if err = experiment.Evaluate(ctx, snapshot, &first, &original); err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(ctx, first); err != nil {
		t.Fatal(err)
	}
	firstStored, err := db.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstStored.Holdout == nil || firstStored.Holdout.Contaminated || firstStored.Verdict == "contaminated" {
		t.Fatalf("first holdout was marked contaminated: %+v", firstStored)
	}
	review := experiment.ReviewRecord(firstStored)
	if review == nil || review.Stats.Samples != firstStored.Result.Development.Research.Holdout.TopRanked.Samples {
		t.Fatal("holdout review did not use the holdout statistics")
	}

	consumed, err = db.HoldoutConsumed(ctx, snapshot.ID)
	if err != nil || !consumed {
		t.Fatalf("first holdout consumption was not persisted: consumed=%v err=%v", consumed, err)
	}
	forged, err := experiment.NewHoldoutRecord(snapshot, original, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Start(ctx, snapshot, forged); err == nil {
		t.Fatal("storage accepted a repeated holdout without contamination")
	}
	second, err := experiment.NewHoldoutRecord(snapshot, original, consumed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Holdout == nil || !second.Holdout.Contaminated {
		t.Fatalf("repeated holdout child is not contaminated: %+v", second.Holdout)
	}
	if second.Holdout.From != first.Holdout.From || second.Holdout.Through != first.Holdout.Through {
		t.Fatal("repeated consumption used a different interval")
	}
	if err = db.Start(ctx, snapshot, second); err != nil {
		t.Fatal(err)
	}
	if err = experiment.Evaluate(ctx, snapshot, &second, &original); err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(ctx, second); err != nil {
		t.Fatal(err)
	}
	secondStored, err := db.Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondStored.Holdout == nil || !secondStored.Holdout.Contaminated || secondStored.Verdict != "contaminated" {
		t.Fatalf("repeated holdout was not marked contaminated: %+v", secondStored)
	}
	if secondStored.Result == nil || !hasResearchFlag(secondStored.Result.Development.Research.Flags, "contaminated") {
		t.Fatal("contaminated holdout result omitted the research flag")
	}

	parentAfter, err := db.Get(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	parentAfterJSON, err := json.Marshal(parentAfter)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parentBefore, parentAfterJSON) {
		t.Fatal("parent record changed after generating holdout children")
	}
}

func TestLegacyHoldoutWithoutFrozenCalendarFailsExplicitly(t *testing.T) {
	snapshot, parent := holdoutLifecycleFixture(t)
	legacy := legacyCalendarSnapshot(t, snapshot)
	parent.Data = legacy.DataSummary
	parent.Status = "completed"
	parent.Result = &experiment.Results{Development: strategy.DevelopmentResult{
		HoldoutFrom:    legacy.Adjusted[0].Bars[len(legacy.Adjusted[0].Bars)-40].Date,
		HoldoutThrough: legacy.AsOf,
	}}
	holdout, err := experiment.NewHoldoutRecord(legacy, parent, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = experiment.Evaluate(context.Background(), legacy, &holdout, &parent); err == nil {
		t.Fatal("legacy snapshot without a frozen trading calendar silently downgraded")
	} else if !strings.Contains(strings.ToLower(err.Error()), "calendar") {
		t.Fatalf("legacy snapshot failed for an unrelated reason: %v", err)
	}
}

func holdoutLifecycleFixture(t *testing.T) (experiment.Snapshot, experiment.Record) {
	t.Helper()
	c := config.Defaults()
	c.RequireCIFS = false
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试", Sector: "银行"}}
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"

	day := time.Date(2026, 1, 2, 0, 0, 0, 0, market.Shanghai)
	bars := []market.Bar{}
	for len(bars) < 190 {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			i := float64(len(bars))
			price := 10 + i*.02 + .1*math.Sin(i)
			bars = append(bars, market.Bar{
				Date: day.Format("2006-01-02"), Open: price, Close: price + .02,
				High: price + .15, Low: price - .1, Volume: 1000,
			})
		}
		day = day.AddDate(0, 0, 1)
	}
	asof := bars[len(bars)-1].Date
	fetchedAt := time.Date(2026, 10, 19, 19, 0, 0, 0, market.Shanghai)
	adjusted := market.History{Version: 1, Code: "sz000001", Name: "测试", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: asof, FetchedAt: fetchedAt, Bars: bars}
	raw := adjusted
	raw.Adjust, raw.DataField = "none", "day"

	snapshot, err := experiment.NewSnapshot(c, asof, []market.History{adjusted}, []market.History{raw})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := experiment.Specification("baseline")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := experiment.NewRecord(snapshot, spec, nil, "holdout-lifecycle-test")
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, parent
}

func legacyCalendarSnapshot(t *testing.T, snapshot experiment.Snapshot) experiment.Snapshot {
	t.Helper()
	snapshot.Calendar.Version = 0
	snapshot.Calendar.Days = nil
	snapshot.Calendar.Source = ""
	snapshot.Calendar.Digest = ""
	snapshot.ID = ""
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	snapshot.ID = hex.EncodeToString(digest[:])
	if err := experiment.ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func hasResearchFlag(flags []string, want string) bool {
	for _, flag := range flags {
		if flag == want {
			return true
		}
	}
	return false
}
