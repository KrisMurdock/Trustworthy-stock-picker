package predictor

import (
	"errors"
)

type TrainOptions struct {
	Hyperparameters
	LabelClip TargetMetrics
}

func DefaultTrainOptions() TrainOptions {
	return TrainOptions{
		Hyperparameters: Hyperparameters{Trees: 120, MaxDepth: 4, MinLeaf: 40, LearningRate: 0.05, L2: 1, Seed: 1949, Bins: 32},
		LabelClip:       TargetMetrics{High: 0.05, Low: 0.05, Close: 0.05},
	}
}

// Train fits one deterministic boosted-tree ensemble per target. The caller
// supplies metadata (feature names, dates, valid-for day) and later fills
// metrics from WalkForward.
func Train(samples []Sample, info ModelInfo, opts TrainOptions) (*Artifact, error) {
	return nil, errors.New("predictor GBDT trainer is not implemented")
}

// Evaluate scores an artifact against samples and returns calibration metrics.
func Evaluate(model *Model, samples []Sample) (ModelMetrics, TargetMetrics, error) {
	return ModelMetrics{}, TargetMetrics{}, errors.New("predictor evaluation is not implemented")
}

// WalkForward trains on each day before a validation day and reports pooled
// out-of-sample metrics plus an absolute-error band for the card.
func WalkForward(samples []Sample, info ModelInfo, validationDays int, opts TrainOptions) (ModelMetrics, TargetMetrics, error) {
	return ModelMetrics{}, TargetMetrics{}, errors.New("predictor walk-forward is not implemented")
}
