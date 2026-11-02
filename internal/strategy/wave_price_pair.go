package strategy

import (
	"math"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const WavePricePairPolicy = "causal_affine_ohlc_v1"

const (
	wavePairMinSessions = 3
	wavePairMaxSessions = 5
	wavePairHalfTick    = .005 // Both providers may round to one fen.
	wavePairEpsilon     = .000001
	wavePairVolumeError = 100.0 // One lot; never a percentage of turnover.
)

// WavePricePairReference freezes an OHLC mapping inferred only from sessions
// ending at the signal. It establishes compatibility, not provider identity or
// the absence/presence of a corporate action. Volume must already be in shares.
// A future mismatch never causes the reference to be refitted.
type WavePricePairReference struct {
	Policy                  string  `json:"policy"`
	Model                   string  `json:"model"`
	Scale                   float64 `json:"scale"`
	Offset                  float64 `json:"offset"`
	PriceTolerance          float64 `json:"price_tolerance"`
	VolumeToleranceShares   float64 `json:"volume_tolerance_shares"`
	EvidenceFrom            string  `json:"evidence_from"`
	EvidenceThrough         string  `json:"evidence_through"`
	EvidenceSessions        int     `json:"evidence_sessions"`
	RawPriceRange           float64 `json:"raw_price_range"`
	AdjustedPriceRange      float64 `json:"adjusted_price_range"`
	MaximumEvidenceResidual float64 `json:"maximum_evidence_residual"`
}

type wavePairPoint struct{ raw, adjusted float64 }

// NewWavePricePairReference requires prefixes ending at the same signal date.
// Only the last five sessions are consulted; at least three matching sessions
// are necessary. Missing/misaligned recent evidence remains unknown rather than
// borrowing older or future prices. A valid raw/qfq prefix may have different
// lengths, for example when raw history covers more years than qfq history.
func NewWavePricePairReference(adjusted, raw []market.Bar) (WavePricePairReference, string) {
	var empty WavePricePairReference
	n := min(len(adjusted), len(raw), wavePairMaxSessions)
	if n < wavePairMinSessions {
		return empty, "pairing_evidence_missing"
	}
	adjusted, raw = adjusted[len(adjusted)-n:], raw[len(raw)-n:]
	points := make([]wavePairPoint, 0, n*4)
	for i := range n {
		if reason := validateWavePairBars(adjusted[i], raw[i]); reason != "" {
			return empty, reason
		}
		if i > 0 && adjusted[i].Date <= adjusted[i-1].Date {
			return empty, "pairing_date_mismatch"
		}
		ap, rp := wavePairPrices(adjusted[i]), wavePairPrices(raw[i])
		for j := range ap {
			points = append(points, wavePairPoint{raw: rp[j], adjusted: ap[j]})
		}
	}
	minRaw, maxRaw, minAdjusted, maxAdjusted := points[0].raw, points[0].raw, points[0].adjusted, points[0].adjusted
	minOffset, maxOffset := points[0].adjusted-points[0].raw, points[0].adjusted-points[0].raw
	var sumRaw, sumAdjusted, sumSquares, sumProduct float64
	for _, p := range points {
		minRaw, maxRaw = math.Min(minRaw, p.raw), math.Max(maxRaw, p.raw)
		minAdjusted, maxAdjusted = math.Min(minAdjusted, p.adjusted), math.Max(maxAdjusted, p.adjusted)
		offset := p.adjusted - p.raw
		minOffset, maxOffset = math.Min(minOffset, offset), math.Max(maxOffset, offset)
		sumRaw += p.raw
		sumAdjusted += p.adjusted
		sumSquares += p.raw * p.raw
		sumProduct += p.raw * p.adjusted
	}
	r := WavePricePairReference{
		Policy: WavePricePairPolicy, VolumeToleranceShares: wavePairVolumeError,
		EvidenceFrom: adjusted[0].Date, EvidenceThrough: adjusted[n-1].Date, EvidenceSessions: n,
		RawPriceRange: maxRaw - minRaw, AdjustedPriceRange: maxAdjusted - minAdjusted,
	}
	// A span smaller than twenty price-rounding bounds does not identify a
	// general mapping reliably. Flat/narrow observations cannot become proof
	// of identity or a convenient affine fit merely because residuals are small.
	if r.RawPriceRange+wavePairEpsilon < .20 || r.AdjustedPriceRange+wavePairEpsilon < .20 {
		return empty, "pairing_insufficient_price_range"
	}
	meanRaw, meanAdjusted := sumRaw/float64(len(points)), sumAdjusted/float64(len(points))
	var variance, covariance float64
	for _, p := range points {
		x, y := p.raw-meanRaw, p.adjusted-meanAdjusted
		variance += x * x
		covariance += x * y
	}
	scale := covariance / variance
	// Compare all supported candidates using only the signal-time evidence.
	// A tolerated translation must not obscure an exact proportional mapping
	// on a narrow price range. Keep the simpler model when improvement in the
	// largest residual is no more than one micro-yuan. The translation's
	// midpoint offset minimizes its largest residual for the fixed unit slope.
	best := empty
	for _, candidate := range []struct {
		model         string
		scale, offset float64
	}{
		{"translation", 1, (minOffset + maxOffset) / 2},
		{"proportional", sumProduct / sumSquares, 0},
		{"affine", scale, meanAdjusted - scale*meanRaw},
	} {
		fitted := r
		if fitted.fit(points, candidate.model, candidate.scale, candidate.offset) &&
			(best.Model == "" || fitted.MaximumEvidenceResidual < best.MaximumEvidenceResidual-wavePairEpsilon) {
			best = fitted
		}
	}
	if best.Model != "" {
		return best, ""
	}
	return empty, "pairing_transform_inconsistent"
}

func (r *WavePricePairReference) fit(points []wavePairPoint, model string, scale, offset float64) bool {
	if !positiveFinite(scale) || math.IsNaN(offset) || math.IsInf(offset, 0) {
		return false
	}
	tolerance := wavePairHalfTick*(1+scale) + wavePairEpsilon
	if !positiveFinite(tolerance) || r.AdjustedPriceRange+wavePairEpsilon < 20*(tolerance-wavePairEpsilon) {
		return false
	}
	var largest float64
	for _, p := range points {
		residual := math.Abs(p.adjusted - math.FMA(scale, p.raw, offset))
		if math.IsNaN(residual) || math.IsInf(residual, 0) || residual > tolerance {
			return false
		}
		largest = math.Max(largest, residual)
	}
	r.Model, r.Scale, r.Offset, r.PriceTolerance, r.MaximumEvidenceResidual = model, scale, offset, tolerance, largest
	return true
}

// Validate checks an observation against the frozen signal-time reference.
// Cent rounding can hide changes smaller than the declared tolerance; larger
// residuals mean only that the price transformation is no longer compatible.
// Rounded evidence need not identify a unique mapping: extrapolation can remain
// unknown even when no actual corporate action occurred.
func (r WavePricePairReference) Validate(adjusted, raw market.Bar) string {
	if r.Policy != WavePricePairPolicy || !positiveFinite(r.Scale) || math.IsNaN(r.Offset) || math.IsInf(r.Offset, 0) ||
		!positiveFinite(r.PriceTolerance) || r.VolumeToleranceShares != wavePairVolumeError ||
		r.EvidenceSessions < wavePairMinSessions || r.EvidenceSessions > wavePairMaxSessions {
		return "pairing_reference_missing"
	}
	switch r.Model {
	case "translation":
		if r.Scale != 1 {
			return "pairing_reference_missing"
		}
	case "proportional":
		if r.Offset != 0 {
			return "pairing_reference_missing"
		}
	case "affine":
	default:
		return "pairing_reference_missing"
	}
	if reason := validateWavePairBars(adjusted, raw); reason != "" {
		return reason
	}
	ap, rp := wavePairPrices(adjusted), wavePairPrices(raw)
	for i := range ap {
		prediction := math.FMA(r.Scale, rp[i], r.Offset)
		if !positiveFinite(prediction) || math.Abs(ap[i]-prediction) > r.PriceTolerance {
			return "price_transform_changed"
		}
	}
	return ""
}

func validateWavePairBars(adjusted, raw market.Bar) string {
	if adjusted.Date != raw.Date {
		return "pairing_date_mismatch"
	}
	date, err := time.Parse(time.DateOnly, adjusted.Date)
	if err != nil || date.Format(time.DateOnly) != adjusted.Date {
		return "pairing_date_mismatch"
	}
	for _, b := range []market.Bar{adjusted, raw} {
		if !positiveFinite(b.Open) || !positiveFinite(b.Close) || !positiveFinite(b.High) || !positiveFinite(b.Low) || !positiveFinite(b.Volume) ||
			b.High < math.Max(b.Open, b.Close) || b.Low > math.Min(b.Open, b.Close) || b.High < b.Low {
			return "pairing_bar_invalid"
		}
	}
	if math.Abs(adjusted.Volume-raw.Volume) > wavePairVolumeError+wavePairEpsilon {
		return "pairing_volume_mismatch"
	}
	return ""
}

func wavePairPrices(b market.Bar) [4]float64 {
	return [4]float64{b.Open, b.Close, b.High, b.Low}
}
