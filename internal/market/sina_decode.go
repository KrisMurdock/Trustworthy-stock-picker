package market

import (
	"math"
	"strings"
	"time"
)

// Sina KLC2 is a little-endian six-bit stream of delta-coded daily bars.
// Protocol reference: AKShare stock/cons.py hk_js_decode, format 3466 v0.
// This decoder reads data only; it never evaluates the response as JavaScript.
type sinaBits struct {
	data []byte
	bit  int
	bad  bool
}

func (b *sinaBits) read(width int, signed bool) int64 {
	if width < 0 || width > 53 || b.bit+width > len(b.data)*6 {
		b.bad = true
		return 0
	}
	var n int64
	for i := 0; i < width; i++ {
		n |= int64((b.data[b.bit/6]>>(b.bit%6))&1) << i
		b.bit++
	}
	if signed && width > 0 && n&(1<<(width-1)) != 0 {
		n -= 1 << width
	}
	return n
}

func (b *sinaBits) flag() bool { return b.read(1, false) != 0 }
func (b *sinaBits) delta() int {
	sign := -1
	if b.flag() {
		sign = 1
	}
	for n := 1; n <= 64; n++ {
		if !b.flag() {
			return n * sign
		}
	}
	b.bad = true
	return 0
}

// Three precision steps are one decimal place; the intermediate steps use
// powers of two and five. Precision changes round stored integers to even.
func sinaScale(p int) float64 {
	sign := 1
	if p < 0 {
		sign, p = -1, -p
	}
	twos, fives := p/3, p/3
	if p%3 == 1 {
		twos++
	}
	if p%3 == 2 {
		fives++
	}
	return math.Pow(2, float64(twos*sign)) * math.Pow(5, float64(fives*sign))
}

func decodeSinaKLC2(encoded string) ([]Bar, error) {
	b := &sinaBits{data: make([]byte, len(encoded))}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := range encoded {
		v := strings.IndexByte(alphabet, encoded[i])
		if v < 0 {
			return nil, &DataError{Reason: "invalid_sina_encoding"}
		}
		b.data[i] = byte(v)
	}
	if b.read(12, false) != 3466 || b.read(6, false) != 63 {
		return nil, &DataError{Reason: "unsupported_sina_encoding"}
	}
	lengths := [8]int{3, 3, 3, 3, 5, 5, 3, 0} // open, high, low, close, volume, amount, post volume/amount
	precision := [5]int{6, 0, 0, 0, 0}        // price, volume, amount, post volume/amount
	var prices [4]float64
	var price, volume float64
	day, weekdays := 0, 62
	separate, post, postExtra := false, false, false
	bars := make([]Bar, 0, 4096)
	advance := func() int {
		n := int(b.read(3, false))
		if n == 1 {
			day = int(b.read(18, true))
			return 0
		}
		if n == 0 {
			return int(b.read(6, false))
		}
		return n
	}
	for !b.bad && len(bars) < 20000 {
		days := 1
		if b.flag() {
			if b.flag() {
				controls := 0
				if b.flag() {
					controls++
					if b.flag() {
						b.flag() // Amount encoding mode is outside the OHLCV contract.
						if b.flag() {
							post = !post
						}
						if b.flag() {
							postExtra = !postExtra
						}
						old := separate
						if b.flag() {
							separate = !separate
						}
						if b.flag() {
							weekdays = int(b.read(7, false))
							if weekdays == 0 {
								b.bad = true
							}
						}
						if old != separate {
							if old {
								price = prices[3]
							} else {
								for i := range prices {
									prices[i] = price
								}
							}
						}
					}
					limit := 3
					if post {
						limit += 2
					}
					for i := 0; i < limit; i++ {
						if b.flag() {
							old := precision[i]
							precision[i] += b.delta()
							if precision[i] < -30 || precision[i] > 30 {
								b.bad = true
							}
							ratio := sinaScale(precision[i]) / sinaScale(old)
							if i == 0 {
								price = math.RoundToEven(price * ratio)
								if separate {
									for j := range prices {
										prices[j] = math.RoundToEven(prices[j] * ratio)
									}
								}
							}
							if i == 1 {
								volume = math.RoundToEven(volume * ratio)
							}
						}
					}
				}
				if b.flag() {
					controls++
					limit := 7
					if post {
						limit++
					}
					if postExtra {
						limit++
					}
					for i := 0; i < limit; i++ {
						if b.flag() {
							if i == 6 {
								days = advance()
							} else {
								index := i
								if i > 6 {
									index--
								}
								lengths[index] += b.delta()
							}
						}
					}
				}
				if b.flag() {
					controls++
					width := lengths[0]
					if b.flag() {
						width += b.delta()
					}
					delta := float64(b.read(3*width, true))
					if !separate {
						price += delta
					}
				}
				if controls == 0 {
					if !b.bad && len(bars) > 0 {
						return bars, nil
					}
					break
				}
			} else if b.flag() {
				if b.flag() {
					if b.flag() {
						days = advance()
					} else {
						lengths[4] += b.delta()
					}
				} else if post && b.flag() {
					index := 6
					if postExtra && b.flag() {
						index++
					}
					lengths[index] += b.delta()
				} else {
					lengths[5] += b.delta()
				}
			} else {
				lengths[b.read(2, false)] += b.delta()
			}
		}
		var values [8]float64
		limit, mask := 6, 185
		if post {
			limit++
		}
		if postExtra {
			limit++
		}
		if separate {
			mask = 191
		}
		for i := 0; i < limit; i++ {
			values[i] = float64(b.read(3*lengths[i], mask&(1<<i) != 0))
		}
		if b.bad {
			break
		}
		for i := 0; i < days; i++ {
			for skipped := 0; ; skipped++ {
				day++
				if weekdays&(1<<((day%7+10)%7)) != 0 {
					break
				}
				if skipped >= 7 {
					b.bad = true
					break
				}
			}
		}
		if day < 0 || day > 80000 {
			b.bad = true
			break
		}
		bar := Bar{Date: time.Unix(int64(7657+day)*86400, 0).UTC().Format("2006-01-02")}
		scale := sinaScale(precision[0])
		if separate {
			for i := range prices {
				prices[i] += values[i]
			}
			bar.Open, bar.High, bar.Low, bar.Close = prices[0]/scale, prices[1]/scale, prices[2]/scale, prices[3]/scale
		} else {
			open := price + values[0]
			price = open + values[3]
			bar.Open, bar.High, bar.Low, bar.Close = open/scale, (open+values[1])/scale, (open-values[2])/scale, price/scale
		}
		volume += values[4]
		bar.Volume = volume / sinaScale(precision[1])
		// Amount/post-market fields are consumed, but are outside our OHLCV contract.
		bars = append(bars, bar)
	}
	return nil, &DataError{Reason: "invalid_sina_stream"}
}
