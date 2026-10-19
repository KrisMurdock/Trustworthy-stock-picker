package market

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func sinaFixture(t *testing.T, code string) ([]byte, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + code + ".sina.js")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := sinaAssignment(raw, "KLC_K2_"+code)
	if err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err := json.Unmarshal(payload, &encoded); err != nil {
		t.Fatal(err)
	}
	return raw, encoded
}

func TestSinaDecoderMatchesReference(t *testing.T) {
	// Digests from the official reference decoder, over every OHLCV field.
	for _, tt := range []struct {
		code, digest string
		count        int
	}{
		{"sh600036", "7700b65702a5a6809f74d4f3102d563f842ca473f01ec6c9398371f8cc872ca1", 5878},
		{"sh688808", "895feec7eba4fe5b3489b43f0aef1b7f25afc35d0ca67616378b979911c78753", 115},
	} {
		t.Run(tt.code, func(t *testing.T) {
			_, encoded := sinaFixture(t, tt.code)
			bars, err := decodeSinaKLC2(encoded)
			if err != nil || len(bars) != tt.count {
				t.Fatalf("decode: count=%d err=%v", len(bars), err)
			}
			h := sha256.New()
			for _, b := range bars {
				fmt.Fprintf(h, "%s,%.8f,%.8f,%.8f,%.8f,%.8f\n", b.Date, b.Open, b.High, b.Low, b.Close, b.Volume)
			}
			if got := fmt.Sprintf("%x", h.Sum(nil)); got != tt.digest {
				t.Fatalf("OHLCV mismatch: %s", got)
			}
			for _, bad := range []string{encoded[:len(encoded)/2], encoded[:len(encoded)-3], "HX/" + encoded[3:], encoded + "!", ""} {
				if _, err := decodeSinaKLC2(bad); err == nil {
					t.Fatal("accepted corrupt/unsupported stream")
				}
			}
		})
	}
}

func TestSinaFetchAdjustmentAndWindow(t *testing.T) {
	raw, _ := sinaFixture(t, "sh688808")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "klc_kl.js") {
			_, _ = w.Write(append(raw, []byte("/* upstream comment */")...))
			return
		}
		adjust := "qfq"
		if strings.HasSuffix(r.URL.Path, "hfq.js") {
			adjust = "hfq"
		}
		fmt.Fprintf(w, `var sh688808%s={"total":2,"data":[{"d":"2026-09-29","f":"1"},{"d":"1900-01-01","f":"2"}]}`+"\n/* no semicolon */", adjust)
	}))
	defer srv.Close()
	c := NewSinaClient(time.Second, 0)
	c.Endpoint = srv.URL
	defer c.Close()
	stock := config.Stock{Code: "sh688808", Name: "测试"}
	base, err := c.Fetch(context.Background(), stock, "2026-09-25", "2026-09-30", "none")
	if err != nil {
		t.Fatal(err)
	}
	for _, adjust := range []string{"qfq", "hfq"} {
		h, err := c.Fetch(context.Background(), stock, "2026-09-25", "2026-09-30", adjust)
		if err != nil || h.Source != "sina" || h.DataField != adjust+"day" || h.Bars[len(h.Bars)-1].Date != "2026-09-30" {
			t.Fatalf("bad adjusted history: %+v %v", h, err)
		}
		for i, b := range h.Bars {
			factor := 1.0
			if b.Date < "2026-09-29" {
				if adjust == "qfq" {
					factor = 0.5
				} else {
					factor = 2
				}
			}
			if b.Close != math.RoundToEven(base.Bars[i].Close*factor*100)/100 || b.Volume != base.Bars[i].Volume {
				t.Fatalf("bad factor/volume: %+v", b)
			}
		}
	}
	h, err := c.FetchWindow(context.Background(), stock, "2026-09-25", "2026-09-30", "none", 2)
	if err != nil || len(h.Bars) != 2 {
		t.Fatalf("window not trimmed: %v %+v", err, h)
	}
}

func TestSinaRejectsMissingOrInvalidFactors(t *testing.T) {
	for _, data := range []string{
		`{"total":0,"data":[]}`, `{"total":2,"data":[{"d":"1900-01-01","f":"1"}]}`,
		`{"total":1,"data":[{"d":"2027-01-01","f":"1"}]}`,
		`{"total":1,"data":[{"d":"1900-01-01","f":"NaN"}]}`,
		`{"total":1,"data":[{"d":"1900-01-01","f":"0"}]}`,
		`{"total":2,"data":[{"d":"1900-01-01","f":"1"},{"d":"1900-01-01","f":"2"}]}`,
	} {
		if err := applySinaFactors([]Bar{{Date: "2026-10-15"}}, []byte(data), "qfq"); err == nil {
			t.Fatalf("accepted factors: %s", data)
		}
	}
	for _, raw := range []string{`var expected=0;/*/`, `var wrong="K2/";`, `var expected=alert(1);`, `var expected="K2/";alert(1)`, `var expected="K2/";/*x*/alert(1)/*x*/`} {
		if _, err := sinaAssignment([]byte(raw), "expected"); err == nil {
			t.Fatal("accepted invalid assignment")
		}
	}
}

func TestSinaHTTPFailuresAndCancellation(t *testing.T) {
	for _, mode := range []string{"403", "429", "timeout", "cancel", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "403":
					w.WriteHeader(403)
				case "429":
					w.WriteHeader(429)
				case "malformed":
					fmt.Fprint(w, `var KLC_K2_sh688808="K2/bad";`)
				default:
					if mode == "cancel" {
						cancel()
					}
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			c := NewSinaClient(40*time.Millisecond, 0)
			c.Endpoint = srv.URL
			defer c.Close()
			_, err := c.Fetch(ctx, config.Stock{Code: "sh688808", Name: "测试"}, "2026-10-12", "2026-10-15", "none")
			if err == nil {
				t.Fatal("accepted failing HTTP source")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if (mode == "429" || mode == "timeout") && IsIsolatable(err) {
				t.Fatalf("lost retryability: %v", err)
			}
		})
	}
}

func FuzzSinaDecoder(f *testing.F) {
	raw, err := os.ReadFile("testdata/sh688808.sina.js")
	if err != nil {
		f.Fatal(err)
	}
	payload, err := sinaAssignment(raw, "KLC_K2_sh688808")
	if err != nil {
		f.Fatal(err)
	}
	var seed string
	if err := json.Unmarshal(payload, &seed); err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add("K2/")
	f.Add("")
	f.Add("HX/invalid")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 8192 {
			t.Skip()
		}
		_, _ = decodeSinaKLC2(s)
	})
}
