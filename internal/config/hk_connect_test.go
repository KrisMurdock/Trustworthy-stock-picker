package config

import (
	"strings"
	"testing"
)

func TestHKConnectValidationAndRoute(t *testing.T) {
	defaults := DefaultHKConnect()
	if err := defaults.Validate(); err != nil {
		t.Fatalf("default HK Connect config is invalid: %v", err)
	}
	if defaults.TopN != 10 {
		t.Fatalf("default HK Connect top_n = %d, want 10", defaults.TopN)
	}
	if err := (HKConnect{Enabled: false, PushEnabled: true, TopN: 5}).Validate(); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("push without enabled error = %v", err)
	}
	for _, topN := range []int{0, 21} {
		if err := (HKConnect{Enabled: true, TopN: topN}).Validate(); err == nil {
			t.Fatalf("top_n=%d unexpectedly accepted", topN)
		}
	}
	if !validNotifyRoute(NotifyRouteHKConnect) {
		t.Fatal("hk_connect is not a valid notification route")
	}
}

func TestBuiltinEnablesIndependentHKConnectArtifactsAndPush(t *testing.T) {
	c := Builtin()
	if !c.HKConnect.Enabled || !c.HKConnect.PushEnabled || c.HKConnect.TopN != 10 {
		t.Fatalf("builtin HK Connect = %+v", c.HKConnect)
	}
	c.Wechat.Enabled = true
	found := false
	for _, name := range c.ChannelNamesForRoute(NotifyRouteHKConnect) {
		found = found || name == WechatDraftChannelName
	}
	if !found {
		t.Fatalf("HK Connect article channels = %v", c.ChannelNamesForRoute(NotifyRouteHKConnect))
	}
}
