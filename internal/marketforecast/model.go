package marketforecast

import "math"

const (
	logisticIterations   = 1500
	logisticLearningRate = 0.12
	logisticL2           = 0.02
)

// directionModel is a small L2-regularized binary logistic regression. Numeric
// features are standardized on each fit; weekday indicators stay 0/1. The batch
// optimizer, iteration count, and penalty are fixed so the output is replayable.
type directionModel struct {
	mean    []float64
	scale   []float64
	weights []float64
}

func fitModel(samples []sample) *directionModel {
	nFeatures := len(FeatureNames())
	numeric := len(numericFeatureNames)
	model := &directionModel{
		mean: make([]float64, numeric), scale: make([]float64, numeric),
		weights: make([]float64, nFeatures+1),
	}
	if len(samples) == 0 {
		for index := range model.scale {
			model.scale[index] = 1
		}
		return model
	}
	for _, item := range samples {
		for index := 0; index < numeric; index++ {
			model.mean[index] += item.features[index]
		}
	}
	for index := range model.mean {
		model.mean[index] /= float64(len(samples))
	}
	for _, item := range samples {
		for index := 0; index < numeric; index++ {
			delta := item.features[index] - model.mean[index]
			model.scale[index] += delta * delta
		}
	}
	for index := range model.scale {
		model.scale[index] = math.Sqrt(model.scale[index] / float64(len(samples)))
		if model.scale[index] < 1e-9 {
			model.scale[index] = 1
		}
	}
	x := make([][]float64, len(samples))
	for row, item := range samples {
		x[row] = model.transform(item.features)
	}
	gradient := make([]float64, len(model.weights))
	count := float64(len(samples))
	for iteration := 0; iteration < logisticIterations; iteration++ {
		for index := range gradient {
			gradient[index] = 0
		}
		for row, features := range x {
			z := model.weights[0]
			for index, value := range features {
				z += model.weights[index+1] * value
			}
			errorValue := sigmoid(z) - float64(samples[row].label)
			gradient[0] += errorValue
			for index, value := range features {
				gradient[index+1] += errorValue * value
			}
		}
		model.weights[0] -= logisticLearningRate * gradient[0] / count
		for index := 1; index < len(model.weights); index++ {
			model.weights[index] -= logisticLearningRate * (gradient[index]/count + logisticL2*model.weights[index])
		}
	}
	return model
}

func (m *directionModel) transform(features []float64) []float64 {
	out := make([]float64, len(features))
	for index, value := range features {
		if index < len(m.mean) {
			out[index] = (value - m.mean[index]) / m.scale[index]
		} else {
			out[index] = value
		}
	}
	return out
}

func (m *directionModel) probability(features []float64) float64 {
	values := m.transform(features)
	z := m.weights[0]
	for index, value := range values {
		z += m.weights[index+1] * value
	}
	return sigmoid(z)
}

func sigmoid(value float64) float64 {
	if value >= 0 {
		return 1 / (1 + math.Exp(-value))
	}
	exponential := math.Exp(value)
	return exponential / (1 + exponential)
}
