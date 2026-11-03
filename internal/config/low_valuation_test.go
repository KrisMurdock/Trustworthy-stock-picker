package config

import "testing"

func TestLowValuationDefaultsAndValidation(t *testing.T) {
	defaults := DefaultLowValuation()
	if defaults.Enabled || defaults.PushEnabled || defaults.TopN != 20 {
		t.Fatalf("defaults = %+v", defaults)
	}
	if err := defaults.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []LowValuation{
		{Enabled: false, PushEnabled: true, TopN: 20},
		{Enabled: true, TopN: 0},
		{Enabled: true, TopN: 51},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
}

func TestBuiltinEnablesLowValuationArticle(t *testing.T) {
	c := Builtin()
	if !c.LowValuation.Enabled || !c.LowValuation.PushEnabled || c.LowValuation.TopN != 20 {
		t.Fatalf("builtin low valuation = %+v", c.LowValuation)
	}
	c.Wechat.Enabled = true
	if got := c.ChannelNamesForRoute(NotifyRouteLowValuation); len(got) != 2 || got[1] != WechatDraftChannelName {
		t.Fatalf("low valuation route = %v", got)
	}
}

func TestLowValuationEnvironmentOverrides(t *testing.T) {
	c := Defaults()
	t.Setenv("STOCK_WATCHER_LOW_VALUATION_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_LOW_VALUATION_PUSH_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_LOW_VALUATION_TOP_N", "7")
	if err := applyEnvironment(&c); err != nil {
		t.Fatal(err)
	}
	if !c.LowValuation.Enabled || !c.LowValuation.PushEnabled || c.LowValuation.TopN != 7 {
		t.Fatalf("environment override = %+v", c.LowValuation)
	}
}
