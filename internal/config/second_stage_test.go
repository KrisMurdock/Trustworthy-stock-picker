package config

import (
	"strings"
	"testing"
)

func TestSecondStageDefaultsAndBuiltin(t *testing.T) {
	second := DefaultSecondStage()
	if second.Enabled || second.PushEnabled || second.TopN != 10 {
		t.Fatalf("unexpected defaults: %+v", second)
	}
	builtin := Builtin()
	if !builtin.SecondStage.Enabled || !builtin.SecondStage.PushEnabled || builtin.SecondStage.TopN != 10 {
		t.Fatalf("production second-stage is not enabled: %+v", builtin.SecondStage)
	}
}

func TestSecondStageValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SecondStage)
		want   string
	}{
		{"top n zero", func(second *SecondStage) { second.Enabled = true; second.TopN = 0 }, "top_n"},
		{"top n too large", func(second *SecondStage) { second.Enabled = true; second.TopN = 101 }, "top_n"},
		{"push without enabled", func(second *SecondStage) { second.Enabled = false; second.PushEnabled = true }, "push_enabled requires"},
	} {
		t.Run(test.name, func(t *testing.T) {
			second := DefaultSecondStage()
			test.mutate(&second)
			if err := second.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	c := Builtin()
	c.FirstStage.Enabled = false
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "requires first_stage.enabled") {
		t.Fatalf("second-stage without first-stage accepted: %v", err)
	}
}

func TestSecondStageEnvironmentOverrides(t *testing.T) {
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_PUSH_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_TOP_N", "7")
	c := Builtin()
	if err := applyEnvironment(&c); err != nil {
		t.Fatal(err)
	}
	if c.SecondStage.Enabled || c.SecondStage.PushEnabled || c.SecondStage.TopN != 7 {
		t.Fatalf("environment did not override second-stage: %+v", c.SecondStage)
	}
}
