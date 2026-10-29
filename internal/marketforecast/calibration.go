package marketforecast

import "math"

const (
	calibrationMethod         = "platt_logit_v1"
	calibrationMinSamples     = 30
	calibrationIterations     = 800
	calibrationLearningRate   = 0.08
	calibrationL2             = 0.02
	calibrationMinSlope       = 0.05
	calibrationMaxSlope       = 5.0
	calibrationMaxIntercept   = 5.0
	calibrationProbabilityMin = 1e-6
)

type calibrationPair struct {
	probability float64
	label       int
}

// probabilityCalibrator maps a tree leaf probability to an out-of-fold
// observed frequency. The identity mapping is used until enough walk-forward
// predictions exist, so early samples cannot be calibrated with future data.
type probabilityCalibrator struct {
	slope     float64
	intercept float64
	samples   int
	status    string
	brier     float64
	ece       float64
}

func fitProbabilityCalibrator(pairs []calibrationPair) probabilityCalibrator {
	out := probabilityCalibrator{slope: 1, samples: len(pairs), status: "insufficient_history"}
	if len(pairs) < calibrationMinSamples {
		return out
	}
	for _, pair := range pairs {
		if !finite(pair.probability) || pair.probability <= 0 || pair.probability >= 1 || (pair.label != 0 && pair.label != 1) {
			return probabilityCalibrator{slope: 1, samples: len(pairs), status: "invalid_history"}
		}
	}
	out.status = "ok"
	out.slope, out.intercept = 1, 0
	for iteration := 0; iteration < calibrationIterations; iteration++ {
		var slopeGradient, interceptGradient float64
		for _, pair := range pairs {
			logit := calibrationLogit(pair.probability)
			errorValue := calibrationSigmoid(out.slope*logit+out.intercept) - float64(pair.label)
			slopeGradient += errorValue * logit
			interceptGradient += errorValue
		}
		slopeGradient = slopeGradient/float64(len(pairs)) + calibrationL2*out.slope
		interceptGradient /= float64(len(pairs))
		out.slope -= calibrationLearningRate * slopeGradient
		out.intercept -= calibrationLearningRate * interceptGradient
		out.slope = clamp(out.slope, calibrationMinSlope, calibrationMaxSlope)
		out.intercept = clamp(out.intercept, -calibrationMaxIntercept, calibrationMaxIntercept)
	}
	calibrated := make([]calibrationPair, len(pairs))
	for i, pair := range pairs {
		calibrated[i] = calibrationPair{probability: out.apply(pair.probability), label: pair.label}
	}
	out.brier = calibrationBrier(calibrated)
	out.ece = calibrationECE(calibrated)
	return out
}

func (c probabilityCalibrator) apply(probability float64) float64 {
	if c.samples == 0 || c.status != "ok" {
		return probability
	}
	return calibrationSigmoid(c.slope*calibrationLogit(probability) + c.intercept)
}

func calibrationLogit(probability float64) float64 {
	probability = clamp(probability, calibrationProbabilityMin, 1-calibrationProbabilityMin)
	return math.Log(probability / (1 - probability))
}

func calibrationSigmoid(value float64) float64 {
	if value >= 0 {
		return 1 / (1 + math.Exp(-value))
	}
	exp := math.Exp(value)
	return exp / (1 + exp)
}

func calibrationBrier(pairs []calibrationPair) float64 {
	if len(pairs) == 0 {
		return 0
	}
	var total float64
	for _, pair := range pairs {
		delta := pair.probability - float64(pair.label)
		total += delta * delta
	}
	return total / float64(len(pairs))
}

func calibrationECE(pairs []calibrationPair) float64 {
	if len(pairs) == 0 {
		return 0
	}
	type bucket struct {
		probabilitySum float64
		count          int
	}
	buckets := [10]bucket{}
	for _, pair := range pairs {
		index := int(pair.probability * 10)
		if index > 9 {
			index = 9
		}
		buckets[index].probabilitySum += pair.probability - float64(pair.label)
		buckets[index].count++
	}
	var total float64
	for _, item := range buckets {
		if item.count == 0 {
			continue
		}
		total += math.Abs(item.probabilitySum/float64(item.count)) * float64(item.count) / float64(len(pairs))
	}
	return total
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
