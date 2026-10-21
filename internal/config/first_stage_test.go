package config

import (
	"strings"
	"testing"
)

func TestFirstStageDefaultsAndBuiltin(t *testing.T) {
	first := DefaultFirstStage()
	if first.Enabled || first.PushEnabled || first.Budget != 95 {
		t.Fatalf("unexpected first-stage defaults: %+v", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	builtin := Builtin()
	if !builtin.FirstStage.Enabled || !builtin.FirstStage.PushEnabled || builtin.FirstStage.Budget != 95 {
		t.Fatalf("production first-stage is not enabled: %+v", builtin.FirstStage)
	}
}

func TestFirstStageValidationRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*FirstStage)
		want   string
	}{
		{"push without enabled", func(first *FirstStage) { first.Enabled = false; first.PushEnabled = true }, "push_enabled requires"},
		{"budget zero", func(first *FirstStage) { first.Enabled = true; first.Budget = 0 }, "budget"},
		{"budget too large", func(first *FirstStage) { first.Enabled = true; first.Budget = 501 }, "budget"},
	} {
		first := DefaultFirstStage()
		tc.mutate(&first)
		if err := first.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v", tc.name, err)
		}
	}
	c := Defaults()
	c.Stocks = []Stock{{Code: "sz000001", Name: "test"}}
	c.FirstStage.Enabled = true
	c.Daily.Adjust = "none"
	c.LLM.ConfigFile = "/tmp/llm.config"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "first_stage requires daily.adjust=qfq") {
		t.Fatalf("non-qfq first-stage accepted: %v", err)
	}
}

func TestFirstStageEnvironmentOverrides(t *testing.T) {
	t.Setenv("STOCK_WATCHER_FIRST_STAGE_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_FIRST_STAGE_PUSH_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_FIRST_STAGE_BUDGET", "140")
	c := Builtin()
	if err := applyEnvironment(&c); err != nil {
		t.Fatal(err)
	}
	if c.FirstStage.Enabled || c.FirstStage.PushEnabled || c.FirstStage.Budget != 140 {
		t.Fatalf("environment did not override first-stage: %+v", c.FirstStage)
	}
}
