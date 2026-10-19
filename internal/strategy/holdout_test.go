package strategy

import (
	"context"
	"testing"
)

func TestHoldoutConsumesOnlyReservedDatesWithFrozenCutoff(t *testing.T) {
	c, h, raw := researchFixture()
	dev, err := AnalyzeDevelopment(context.Background(), c, h, raw, h[0].AsOf, EntryPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if dev.HoldoutFrom == "" || dev.HoldoutThrough == "" {
		t.Fatal("development did not reserve a holdout")
	}
	hold, err := AnalyzeHoldout(context.Background(), c, h, raw, h[0].AsOf, EntryPolicy{}, dev.HoldoutFrom, dev.HoldoutThrough, false)
	if err != nil {
		t.Fatal(err)
	}
	if hold.Research.Holdout.Method != "frozen_holdout" || hold.Research.Development.Method != "frozen_training" {
		t.Fatalf("holdout was not isolated from training: %+v", hold.Research)
	}
	if len(hold.Trades) == 0 {
		t.Fatal("holdout produced no candidate evidence")
	}
	for _, trade := range hold.Trades {
		if trade.Date < dev.HoldoutFrom || trade.Date > dev.HoldoutThrough {
			t.Fatalf("trade outside reserved interval: %+v", trade)
		}
	}
	if hold.Research.TrainingThrough >= dev.HoldoutFrom {
		t.Fatalf("training labels leaked into holdout: %s", hold.Research.TrainingThrough)
	}
	if hold.Research.ValidationPassed {
		t.Fatal("small synthetic holdout unexpectedly passed validation")
	}
	again, err := AnalyzeHoldout(context.Background(), c, h, raw, h[0].AsOf, EntryPolicy{}, dev.HoldoutFrom, dev.HoldoutThrough, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Research.ValidationPassed || again.Research.Verdict != "contaminated" || !hasFlag(again.Research.Flags, "contaminated") {
		t.Fatalf("repeated holdout was not marked contaminated: %+v", again.Research)
	}
}
