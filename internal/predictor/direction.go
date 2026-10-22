package predictor

import (
	"errors"
	"math"
)

// DefaultDirectionThreshold is the minimum absolute distance from 0.5 before
// the direction model is allowed to correct the regression close sign. A value
// of 1 disables the correction while retaining the trained direction model.
const DefaultDirectionThreshold = 0.02

const (
	directionMaxSamples = 250000
	directionBatchSize  = 4096
	directionEpochs     = 30
	directionLearnRate  = 0.02
	directionL2         = 50.0
)

// DirectionModel is a standardized logistic classifier for the sign of the
// next five-minute close return. It is deliberately separate from the three
// boosted-tree point regressors: the close point estimate has little directional
// signal, while a binary classifier can learn a usable sign probability.
type DirectionModel struct {
	Bias                float64   `json:"bias"`
	Weights             []float64 `json:"weights"`
	Means               []float64 `json:"means"`
	Stds                []float64 `json:"stds"`
	ConfidenceThreshold float64   `json:"confidence_threshold"`
	Samples             int       `json:"samples"`
	UpRate              float64   `json:"up_rate"`
}

func directionThreshold(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return DefaultDirectionThreshold
	}
	if value > 1 {
		return 1
	}
	return value
}

func (d *DirectionModel) valid(nFeatures int) bool {
	if d == nil || nFeatures <= 0 || len(d.Weights) != nFeatures || len(d.Means) != nFeatures || len(d.Stds) != nFeatures {
		return false
	}
	if math.IsNaN(d.Bias) || math.IsInf(d.Bias, 0) || math.IsNaN(d.ConfidenceThreshold) || math.IsInf(d.ConfidenceThreshold, 0) ||
		d.ConfidenceThreshold < 0 || d.ConfidenceThreshold > 1 {
		return false
	}
	if d.Samples < 2 || math.IsNaN(d.UpRate) || math.IsInf(d.UpRate, 0) || d.UpRate <= 0 || d.UpRate >= 1 {
		return false
	}
	for i := range d.Weights {
		if math.IsNaN(d.Weights[i]) || math.IsInf(d.Weights[i], 0) ||
			math.IsNaN(d.Means[i]) || math.IsInf(d.Means[i], 0) ||
			math.IsNaN(d.Stds[i]) || math.IsInf(d.Stds[i], 0) || d.Stds[i] <= 0 {
			return false
		}
	}
	return true
}

func (d *DirectionModel) probability(values []float64) (float64, error) {
	if d == nil || len(values) != len(d.Weights) {
		return 0, errors.New("predictor direction feature count mismatch")
	}
	z := d.Bias
	for i, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		z += d.Weights[i] * (value - d.Means[i]) / d.Stds[i]
	}
	if math.IsNaN(z) || math.IsInf(z, 0) {
		return 0, errors.New("predictor direction produced a non-finite value")
	}
	if z > 40 {
		return 1, nil
	}
	if z < -40 {
		return 0, nil
	}
	return 1 / (1 + math.Exp(-z)), nil
}

func fitDirectionModel(samples []Sample, nFeatures int, threshold float64) *DirectionModel {
	threshold = directionThreshold(threshold)
	if len(samples) == 0 || nFeatures <= 0 {
		return nil
	}
	means := make([]float64, nFeatures)
	stds := make([]float64, nFeatures)
	counts := make([]int, nFeatures)
	sums := make([]float64, nFeatures)
	squares := make([]float64, nFeatures)
	for _, sample := range samples {
		if len(sample.Features) != nFeatures {
			return nil
		}
		for i, value := range sample.Features {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			counts[i]++
			sums[i] += value
			squares[i] += value * value
		}
	}
	for i := 0; i < nFeatures; i++ {
		if counts[i] == 0 {
			means[i] = 0
			stds[i] = 1
			continue
		}
		means[i] = sums[i] / float64(counts[i])
		variance := squares[i]/float64(counts[i]) - means[i]*means[i]
		if variance < 1e-12 || math.IsNaN(variance) || math.IsInf(variance, 0) {
			variance = 1
		}
		stds[i] = math.Sqrt(variance)
	}

	indices := make([]int, 0, len(samples))
	up := 0
	for i, sample := range samples {
		label, ok := sample.Labels[TargetClose]
		if !ok || math.IsNaN(label) || math.IsInf(label, 0) || label == 0 {
			continue
		}
		indices = append(indices, i)
		if label > 0 {
			up++
		}
	}
	if len(indices) < 64 || up == 0 || up == len(indices) {
		return nil
	}
	if len(indices) > directionMaxSamples {
		step := float64(len(indices)) / float64(directionMaxSamples)
		limited := make([]int, 0, directionMaxSamples)
		for k := 0; k < directionMaxSamples; k++ {
			limited = append(limited, indices[int(float64(k)*step)])
		}
		indices = limited
		up = 0
		for _, index := range indices {
			if samples[index].Labels[TargetClose] > 0 {
				up++
			}
		}
	}

	rows := make([]float64, len(indices)*nFeatures)
	labels := make([]float64, len(indices))
	for row, index := range indices {
		sample := samples[index]
		for f, value := range sample.Features {
			standardized := 0.0
			if !math.IsNaN(value) && !math.IsInf(value, 0) {
				standardized = (value - means[f]) / stds[f]
			}
			rows[row*nFeatures+f] = standardized
		}
		if sample.Labels[TargetClose] > 0 {
			labels[row] = 1
		}
	}

	weights := make([]float64, nFeatures)
	momentum := make([]float64, nFeatures)
	velocity := make([]float64, nFeatures)
	bias, biasMomentum, biasVelocity := 0.0, 0.0, 0.0
	order := make([]int, len(indices))
	for i := range order {
		order[i] = i
	}
	shuffleDirectionRows(order)
	step := 0
	regularization := directionL2 / float64(len(indices))
	for epoch := 0; epoch < directionEpochs; epoch++ {
		for start := 0; start < len(order); start += directionBatchSize {
			end := start + directionBatchSize
			if end > len(order) {
				end = len(order)
			}
			gradW := make([]float64, nFeatures)
			gradB := 0.0
			for _, row := range order[start:end] {
				z := bias
				base := row * nFeatures
				for f := 0; f < nFeatures; f++ {
					z += weights[f] * rows[base+f]
				}
				probability := 0.0
				if z > 40 {
					probability = 1
				} else if z >= -40 {
					probability = 1 / (1 + math.Exp(-z))
				}
				delta := probability - labels[row]
				gradB += delta
				for f := 0; f < nFeatures; f++ {
					gradW[f] += delta * rows[base+f]
				}
			}
			batch := float64(end - start)
			gradB /= batch
			for f := 0; f < nFeatures; f++ {
				gradW[f] = gradW[f]/batch + regularization*weights[f]
			}
			step++
			beta1, beta2 := 0.9, 0.999
			correction1 := 1 - math.Pow(beta1, float64(step))
			correction2 := 1 - math.Pow(beta2, float64(step))
			biasMomentum = beta1*biasMomentum + (1-beta1)*gradB
			biasVelocity = beta2*biasVelocity + (1-beta2)*gradB*gradB
			bias -= directionLearnRate * (biasMomentum / correction1) / (math.Sqrt(biasVelocity/correction2) + 1e-8)
			for f := 0; f < nFeatures; f++ {
				momentum[f] = beta1*momentum[f] + (1-beta1)*gradW[f]
				velocity[f] = beta2*velocity[f] + (1-beta2)*gradW[f]*gradW[f]
				weights[f] -= directionLearnRate * (momentum[f] / correction1) / (math.Sqrt(velocity[f]/correction2) + 1e-8)
			}
		}
	}
	model := &DirectionModel{
		Bias: bias, Weights: weights, Means: means, Stds: stds,
		ConfidenceThreshold: threshold, Samples: len(indices), UpRate: float64(up) / float64(len(indices)),
	}
	if !model.valid(nFeatures) {
		return nil
	}
	return model
}

func shuffleDirectionRows(order []int) {
	state := uint64(1949)
	for i := len(order) - 1; i > 0; i-- {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		j := int(state % uint64(i+1))
		order[i], order[j] = order[j], order[i]
	}
}
