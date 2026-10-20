package main

import "testing"

func TestMinuteProviderForRange(t *testing.T) {
	for _, tt := range []struct {
		provider string
		days     int
		want     string
		ok       bool
	}{
		{provider: "auto", days: 104, want: "sina", ok: true},
		{provider: "auto", days: 105, want: "baostock", ok: true},
		{provider: "sina", days: 104, want: "sina", ok: true},
		{provider: "sina", days: 105, ok: false},
		{provider: "baostock", days: 500, want: "baostock", ok: true},
		{provider: "other", days: 10, ok: false},
	} {
		got, err := minuteProviderForRange(tt.provider, tt.days)
		if (err == nil) != tt.ok || (tt.ok && got != tt.want) {
			t.Fatalf("minuteProviderForRange(%q, %d) = %q, %v", tt.provider, tt.days, got, err)
		}
	}
}
