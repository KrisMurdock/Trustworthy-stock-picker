package config

import "testing"

func TestAnalysisConfiguration(t *testing.T) {
	for _, a := range []Analysis{{Engine: "unknown", Timeout: "1m", SyncTimeout: "1h"}, {Engine: "auto", Timeout: "0s", SyncTimeout: "1h"}, {Engine: "auto", Timeout: "1m", SyncTimeout: "25h"}} {
		if a.Validate() == nil {
			t.Fatal("invalid configuration accepted", a)
		}
	}
	if err := Defaults().Analysis.Validate(); err != nil {
		t.Fatal(err)
	}
}
