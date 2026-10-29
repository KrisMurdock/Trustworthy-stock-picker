package marketforecast

import (
	"encoding/json"
	"testing"
)

func hangSengTestBars(bars []Bar) []Bar {
	out := make([]Bar, 0, len(bars))
	for index, bar := range bars {
		adjust := 1 + float64(index%9-4)*0.0008
		out = append(out, Bar{
			Date: bar.Date, Open: 19000 * adjust, Close: 19100 * adjust,
			High: 19200 * adjust, Low: 18900 * adjust,
		})
	}
	return out
}

func TestSessionShadowAndHangSengDoNotChangeFormalSignal(t *testing.T) {
	bars := sessionTestBars(320)
	minutes := sessionTestMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	base := buildSessionSignals(bars, minutes, nil, target, koreaContext{}, false, hangSengContext{}, false)
	hangSeng, ok := newHangSengContext(hangSengTestBars(bars))
	if !ok {
		t.Fatal("hang seng fixture rejected")
	}
	withShadow := buildSessionSignals(bars, minutes, nil, target, koreaContext{}, false, hangSeng, true)
	if len(base) != 2 || len(withShadow) != 2 {
		t.Fatalf("unexpected session count: base=%d shadow=%d", len(base), len(withShadow))
	}
	for index := range base {
		if base[index].Status != withShadow[index].Status || base[index].Direction != withShadow[index].Direction || base[index].UpProbability != withShadow[index].UpProbability || base[index].HighConfidence != withShadow[index].HighConfidence {
			t.Fatalf("hang seng shadow changed formal session: base=%+v shadow=%+v", base[index], withShadow[index])
		}
	}
	morning := withShadow[0]
	if morning.Shadow == nil || morning.Shadow.HangSeng != nil {
		t.Fatalf("morning shadow unexpectedly uses hang seng: %+v", morning.Shadow)
	}
	afternoon := withShadow[1]
	if afternoon.Shadow == nil || afternoon.Shadow.MultiWindow == nil || afternoon.Shadow.MultiWindow.Status != "ok" {
		t.Fatalf("multi-window shadow unavailable: shadow=%+v ensemble=%+v", afternoon.Shadow, afternoon.Shadow.MultiWindow)
	}
	ensemble := afternoon.Shadow.MultiWindow
	if len(ensemble.ByWindow) != len(SessionShadowWindows) || len(ensemble.ByYear) == 0 || ensemble.Validation.Samples == 0 || ensemble.Validation.Predictions == 0 {
		t.Fatalf("incomplete shadow evidence: %+v", ensemble)
	}
	if ensemble.Validation.Coverage <= 0 || ensemble.Validation.Coverage > 1 || ensemble.Validation.Accuracy < 0 || ensemble.Validation.Accuracy > 1 {
		t.Fatalf("invalid shadow validation: %+v", ensemble.Validation)
	}
	if afternoon.Shadow.HangSeng == nil || afternoon.Shadow.HangSeng.Status != "ok" || !containsString(afternoon.Shadow.HangSeng.FeatureNames, "hsi_ret1") {
		t.Fatalf("hang seng shadow unavailable: %+v", afternoon.Shadow.HangSeng)
	}
}

func TestHangSengShadowIgnoresTargetDayAndMarksStale(t *testing.T) {
	bars := sessionTestBars(320)
	minutes := sessionTestMinutes(bars)
	target, _ := nextWeekday(bars[len(bars)-1].Date)
	hangSengBars := hangSengTestBars(bars)
	first, _ := newHangSengContext(hangSengBars)
	second, _ := newHangSengContext(append(append([]Bar(nil), hangSengBars...), Bar{
		Date: target, Open: 25000, Close: 26000, High: 27000, Low: 24000,
	}))
	before, _ := json.Marshal(buildSessionSignals(bars, minutes, nil, target, koreaContext{}, false, first, true))
	after, _ := json.Marshal(buildSessionSignals(bars, minutes, nil, target, koreaContext{}, false, second, true))
	if string(before) != string(after) {
		t.Fatal("target-day Hang Seng bar changed the pre-open shadow")
	}

	staleBars := append([]Bar(nil), hangSengBars...)
	staleBars = staleBars[:len(staleBars)-20]
	stale, ok := newHangSengContext(staleBars)
	if !ok {
		t.Fatal("stale hang seng fixture rejected")
	}
	signals := buildSessionSignals(bars, minutes, nil, target, koreaContext{}, false, stale, true)
	if signals[1].Shadow == nil || signals[1].Shadow.HangSeng == nil || signals[1].Shadow.HangSeng.Status != "stale" {
		t.Fatalf("stale hang seng was not surfaced: %+v", signals[1].Shadow)
	}
}
