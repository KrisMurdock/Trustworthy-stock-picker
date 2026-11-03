package config

import "testing"

func TestValueTrappedDefaultsAndValidation(t *testing.T) {
	defaults := DefaultValueTrapped()
	if defaults.Enabled || defaults.PushEnabled || defaults.TopN != 20 || defaults.MaxTrappedRatio != 0.30 {
		t.Fatalf("defaults = %+v", defaults)
	}
	if err := defaults.Validate(true); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		value ValueTrapped
		low   bool
	}{
		{value: ValueTrapped{Enabled: true, TopN: 20, MaxTrappedRatio: 0.30}, low: false},
		{value: ValueTrapped{PushEnabled: true, TopN: 20, MaxTrappedRatio: 0.30}, low: true},
		{value: ValueTrapped{Enabled: true, TopN: 0, MaxTrappedRatio: 0.30}, low: true},
		{value: ValueTrapped{Enabled: true, TopN: 51, MaxTrappedRatio: 0.30}, low: true},
		{value: ValueTrapped{Enabled: true, TopN: 20, MaxTrappedRatio: 0}, low: true},
		{value: ValueTrapped{Enabled: true, TopN: 20, MaxTrappedRatio: 1.1}, low: true},
	} {
		if err := test.value.Validate(test.low); err == nil {
			t.Fatalf("accepted %+v (low enabled=%v)", test.value, test.low)
		}
	}
}

func TestBuiltinEnablesValueTrappedArticle(t *testing.T) {
	c := Builtin()
	if !c.ValueTrapped.Enabled || !c.ValueTrapped.PushEnabled || c.ValueTrapped.TopN != 20 || c.ValueTrapped.MaxTrappedRatio != 0.30 {
		t.Fatalf("builtin value trapped = %+v", c.ValueTrapped)
	}
	c.Wechat.Enabled = true
	if got := c.ChannelNamesForRoute(NotifyRouteValueTrapped); len(got) != 2 || got[1] != WechatDraftChannelName {
		t.Fatalf("value-trapped route = %v", got)
	}
}

func TestValueTrappedEnvironmentOverrides(t *testing.T) {
	c := Defaults()
	t.Setenv("STOCK_WATCHER_LOW_VALUATION_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_VALUE_TRAPPED_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_VALUE_TRAPPED_PUSH_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_VALUE_TRAPPED_TOP_N", "7")
	t.Setenv("STOCK_WATCHER_VALUE_TRAPPED_MAX_TRAPPED_RATIO", "0.25")
	if err := applyEnvironment(&c); err != nil {
		t.Fatal(err)
	}
	if !c.ValueTrapped.Enabled || !c.ValueTrapped.PushEnabled || c.ValueTrapped.TopN != 7 || c.ValueTrapped.MaxTrappedRatio != 0.25 {
		t.Fatalf("environment override = %+v", c.ValueTrapped)
	}
}
