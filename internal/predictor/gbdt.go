package predictor

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

type TrainOptions struct {
	Hyperparameters
	LabelClip   TargetMetrics
	Calibration PredictionCalibration
}

var errNoPredictionSamples = errors.New("predictor has no training samples")

func DefaultTrainOptions() TrainOptions {
	return TrainOptions{
		Hyperparameters: Hyperparameters{Trees: 120, MaxDepth: 4, MinLeaf: 40, LearningRate: 0.05, L2: 1, Seed: 1949, Bins: 32},
		LabelClip:       TargetMetrics{High: 0.05, Low: 0.05, Close: 0.05},
		Calibration:     DefaultPredictionCalibration(),
	}
}

// Train fits one deterministic boosted-tree ensemble per target. The caller
// supplies metadata (feature names, dates, valid-for day) and later fills
// metrics from WalkForward.
func Train(samples []Sample, info ModelInfo, opts TrainOptions) (*Artifact, error) {
	if len(samples) == 0 {
		return nil, errNoPredictionSamples
	}
	opts = normalizeTrainOptions(opts)
	ordered := append([]Sample(nil), samples...)
	sortSamples(ordered)
	names := info.FeatureNames
	if len(names) == 0 {
		names = FeatureNames()
	}
	nFeatures := len(names)
	if nFeatures == 0 {
		return nil, errors.New("predictor model has no features")
	}
	for i := range ordered {
		if len(ordered[i].Features) != nFeatures {
			return nil, fmt.Errorf("predictor sample %d has %d features, want %d", i, len(ordered[i].Features), nFeatures)
		}
		for _, value := range ordered[i].Features {
			if math.IsInf(value, 0) {
				return nil, errors.New("predictor sample contains a non-finite feature")
			}
		}
	}
	info.SchemaVersion = ArtifactSchemaVersion
	info.FeatureVersion = FeatureVersion
	info.FeatureNames = append([]string(nil), names...)
	info.Calibration = &opts.Calibration
	if info.DataFrom == "" {
		info.DataFrom = ordered[0].Date
	}
	if info.DataTo == "" {
		info.DataTo = ordered[len(ordered)-1].Date
	}
	if info.CreatedAt.IsZero() {
		info.CreatedAt = ordered[len(ordered)-1].TargetEnd
	}
	binner := newBinner(ordered, nFeatures, opts.Bins)
	artifact := &Artifact{
		SchemaVersion:   ArtifactSchemaVersion,
		Info:            info,
		Hyperparameters: opts.Hyperparameters,
		Base:            make(map[Target]float64, len(Targets)),
		Trees:           make(map[Target][]Tree, len(Targets)),
		LabelClip:       map[Target]float64{TargetHigh: opts.LabelClip.High, TargetLow: opts.LabelClip.Low, TargetClose: opts.LabelClip.Close},
		Calibration:     &opts.Calibration,
	}
	for _, target := range Targets {
		labels := make([]float64, len(ordered))
		for i := range ordered {
			label, ok := ordered[i].Labels[target]
			if !ok || math.IsNaN(label) || math.IsInf(label, 0) {
				return nil, fmt.Errorf("predictor sample %d is missing a finite %s label", i, target)
			}
			labels[i] = clip(label, opts.LabelClip.value(target))
		}
		base := mean(labels)
		artifact.Base[target] = base
		residual := make([]float64, len(labels))
		for i, label := range labels {
			residual[i] = label - base
		}
		trainer := &treeTrainer{
			residual:   residual,
			features:   ordered,
			binned:     binner.data,
			nFeatures:  nFeatures,
			opts:       opts,
			thresholds: binner.thresholds,
			histC:      make([][]float64, nFeatures),
			histS:      make([][]float64, nFeatures),
			missingBin: opts.Bins + 1,
		}
		for f := 0; f < nFeatures; f++ {
			trainer.histC[f] = make([]float64, opts.Bins+2)
			trainer.histS[f] = make([]float64, opts.Bins+2)
		}
		trees := make([]Tree, 0, opts.Trees)
		for step := 0; step < opts.Trees; step++ {
			tree := trainer.fit()
			trees = append(trees, tree)
			for i := range ordered {
				residual[i] -= opts.LearningRate * predictTree(tree, ordered[i].Features)
			}
		}
		artifact.Trees[target] = trees
	}
	return artifact, nil
}

// Evaluate scores an artifact against samples and returns calibration metrics.
func Evaluate(model *Model, samples []Sample) ([]modeleval.MetricRecord, TargetMetrics, error) {
	if model == nil {
		return nil, TargetMetrics{}, errors.New("predictor model is not loaded")
	}
	acc := newMetricAccumulator()
	for _, sample := range samples {
		if sample.InputClose <= 0 {
			continue
		}
		actual := TargetMetrics{
			High:  sample.Labels[TargetHigh],
			Low:   sample.Labels[TargetLow],
			Close: sample.Labels[TargetClose],
		}
		if !targetMetricsFinite(actual) {
			continue
		}
		predicted, err := model.Predict(sample.Features)
		if err != nil {
			return nil, TargetMetrics{}, err
		}
		acc.add(sample, predicted)
	}
	if acc.sampleCount() == 0 {
		return nil, TargetMetrics{}, errors.New("predictor evaluation has no valid samples")
	}
	subject := modeleval.ModelSubject(model.Artifact.Info.ModelID)
	if strings.TrimSpace(model.Artifact.Info.ModelID) == "" {
		subject = "model:candidate"
	}
	return evaluationRecords(acc, subject), acc.bands(), nil
}

// WalkForward trains on each day before a validation day and reports pooled
// out-of-sample metrics plus an absolute-error band for the card.
func WalkForward(samples []Sample, info ModelInfo, validationDays int, opts TrainOptions) ([]modeleval.MetricRecord, TargetMetrics, error) {
	if len(samples) == 0 {
		return nil, TargetMetrics{}, errNoPredictionSamples
	}
	if validationDays <= 0 {
		validationDays = 3
	}
	ordered := append([]Sample(nil), samples...)
	sortSamples(ordered)
	dates := make([]string, 0, 32)
	for _, sample := range ordered {
		if len(dates) == 0 || dates[len(dates)-1] != sample.Date {
			dates = append(dates, sample.Date)
		}
	}
	if len(dates) < 2 {
		return nil, TargetMetrics{}, errors.New("predictor walk-forward requires at least two trading days")
	}
	start := len(dates) - validationDays
	if start < 1 {
		start = 1
	}
	acc := newMetricAccumulator()
	folds := 0
	for i := start; i < len(dates); i++ {
		date := dates[i]
		train := make([]Sample, 0, len(ordered))
		validation := make([]Sample, 0, 512)
		for _, sample := range ordered {
			switch {
			case sample.Date < date:
				train = append(train, sample)
			case sample.Date == date:
				validation = append(validation, sample)
			}
		}
		if len(train) == 0 || len(validation) == 0 {
			continue
		}
		foldInfo := info
		foldInfo.Metrics = nil
		foldInfo.Residuals = TargetMetrics{}
		artifact, err := Train(train, foldInfo, opts)
		if err != nil {
			return nil, TargetMetrics{}, err
		}
		model := &Model{Artifact: *artifact}
		for _, sample := range validation {
			if sample.InputClose <= 0 {
				continue
			}
			actual := TargetMetrics{
				High:  sample.Labels[TargetHigh],
				Low:   sample.Labels[TargetLow],
				Close: sample.Labels[TargetClose],
			}
			if !targetMetricsFinite(actual) {
				continue
			}
			predicted, err := model.Predict(sample.Features)
			if err != nil {
				return nil, TargetMetrics{}, err
			}
			acc.add(sample, predicted)
		}
		folds++
	}
	if folds == 0 {
		return nil, TargetMetrics{}, errors.New("predictor walk-forward produced no validation folds")
	}
	if acc.sampleCount() == 0 {
		return nil, TargetMetrics{}, errors.New("predictor walk-forward produced no valid validation samples")
	}
	subject := modeleval.ModelSubject(info.ModelID)
	if strings.TrimSpace(info.ModelID) == "" {
		subject = "model:candidate"
	}
	return evaluationRecords(acc, subject), acc.bands(), nil
}

func evaluationRecords(acc *metricAccumulator, subject string) []modeleval.MetricRecord {
	records := acc.records(subject, nil, func(sample metricSample) TargetMetrics { return sample.Predicted }, true)
	records = append(records, acc.records(modeleval.BaselineSubject(modeleval.BaselineLastPrice), nil,
		func(sample metricSample) TargetMetrics { return sample.Last }, false)...)
	records = append(records, acc.records(modeleval.BaselineSubject(modeleval.BaselineRange), nil,
		func(sample metricSample) TargetMetrics { return sample.Range }, false)...)
	records = append(records, acc.majorityRecords(modeleval.BaselineSubject(modeleval.BaselineMajority), nil)...)
	return sortUniqueRecords(records)
}

func normalizeTrainOptions(opts TrainOptions) TrainOptions {
	defaults := DefaultTrainOptions()
	if opts.Trees <= 0 {
		opts.Trees = defaults.Trees
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = defaults.MaxDepth
	}
	if opts.MaxDepth > 10 {
		opts.MaxDepth = 10
	}
	if opts.MinLeaf <= 0 {
		opts.MinLeaf = 1
	}
	if opts.LearningRate <= 0 {
		opts.LearningRate = defaults.LearningRate
	}
	if opts.LearningRate > 1 {
		opts.LearningRate = 1
	}
	if opts.L2 < 0 {
		opts.L2 = 0
	}
	if opts.Bins < 2 {
		opts.Bins = defaults.Bins
	}
	if opts.Bins > 64 {
		opts.Bins = 64
	}
	if opts.LabelClip.High <= 0 {
		opts.LabelClip.High = defaults.LabelClip.High
	}
	if opts.LabelClip.Low <= 0 {
		opts.LabelClip.Low = defaults.LabelClip.Low
	}
	if opts.LabelClip.Close <= 0 {
		opts.LabelClip.Close = defaults.LabelClip.Close
	}
	opts.Calibration = opts.Calibration.normalized()
	return opts
}

func (m TargetMetrics) value(target Target) float64 {
	switch target {
	case TargetHigh:
		return m.High
	case TargetLow:
		return m.Low
	case TargetClose:
		return m.Close
	default:
		return 0
	}
}

func (m *TargetMetrics) accumulate(target Target, value float64) {
	switch target {
	case TargetHigh:
		m.High += value
	case TargetLow:
		m.Low += value
	case TargetClose:
		m.Close += value
	}
}

func (m TargetMetrics) divided(n int) TargetMetrics {
	if n <= 0 {
		return TargetMetrics{}
	}
	d := float64(n)
	return TargetMetrics{High: m.High / d, Low: m.Low / d, Close: m.Close / d}
}

func clip(value, limit float64) float64 {
	if limit <= 0 {
		return value
	}
	return math.Max(-limit, math.Min(limit, value))
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

type binner struct {
	thresholds [][]float64
	data       []uint8
}

const missingBin = uint8(255)

func newBinner(samples []Sample, nFeatures, maxBins int) *binner {
	out := &binner{
		thresholds: make([][]float64, nFeatures),
		data:       make([]uint8, len(samples)*nFeatures),
	}
	for f := 0; f < nFeatures; f++ {
		values := make([]float64, 0, len(samples))
		for i := range samples {
			value := samples[i].Features[f]
			if math.IsNaN(value) {
				continue
			}
			values = append(values, value)
		}
		out.thresholds[f] = featureThresholds(values, maxBins)
		for i := range samples {
			value := samples[i].Features[f]
			if math.IsNaN(value) {
				out.data[i*nFeatures+f] = missingBin
				continue
			}
			bin := sort.SearchFloat64s(out.thresholds[f], value)
			if bin > 254 {
				bin = 254
			}
			out.data[i*nFeatures+f] = uint8(bin)
		}
	}
	return out
}

func featureThresholds(values []float64, maxBins int) []float64 {
	if len(values) < 2 {
		return nil
	}
	sort.Float64s(values)
	unique := values[:1]
	for _, value := range values[1:] {
		if value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}
	if len(unique) < 2 {
		return nil
	}
	if len(unique) <= maxBins {
		out := make([]float64, 0, len(unique)-1)
		for i := 1; i < len(unique); i++ {
			out = append(out, (unique[i-1]+unique[i])/2)
		}
		return out
	}
	out := make([]float64, 0, maxBins-1)
	for k := 1; k < maxBins; k++ {
		idx := k * len(unique) / maxBins
		if idx <= 0 || idx >= len(unique) {
			continue
		}
		threshold := unique[idx-1]
		if len(out) == 0 || threshold > out[len(out)-1] {
			out = append(out, threshold)
		}
	}
	return out
}

type treeTrainer struct {
	residual   []float64
	features   []Sample
	binned     []uint8
	nFeatures  int
	opts       TrainOptions
	thresholds [][]float64
	histC      [][]float64
	histS      [][]float64
	missingBin int
	tree       Tree
}

func (t *treeTrainer) fit() Tree {
	t.tree = Tree{Nodes: make([]Node, 0, 64)}
	indices := make([]int32, len(t.features))
	for i := range indices {
		indices[i] = int32(i)
	}
	t.build(indices, 0)
	return t.tree
}

func (t *treeTrainer) build(indices []int32, depth int) int {
	nodeIndex := len(t.tree.Nodes)
	t.tree.Nodes = append(t.tree.Nodes, Node{})
	totalCount := float64(len(indices))
	totalSum := 0.0
	for _, row := range indices {
		totalSum += t.residual[row]
	}
	if depth >= t.opts.MaxDepth || len(indices) < 2*t.opts.MinLeaf {
		t.tree.Nodes[nodeIndex] = Node{Leaf: true, Value: leafValue(totalSum, totalCount, t.opts.L2)}
		return nodeIndex
	}
	for f := 0; f < t.nFeatures; f++ {
		for b := 0; b <= t.opts.Bins+1; b++ {
			t.histC[f][b] = 0
			t.histS[f][b] = 0
		}
	}
	for _, row := range indices {
		value := t.residual[row]
		base := int(row) * t.nFeatures
		for f := 0; f < t.nFeatures; f++ {
			bin := int(t.binned[base+f])
			if bin == int(missingBin) {
				bin = t.missingBin
			}
			t.histC[f][bin]++
			t.histS[f][bin] += value
		}
	}
	bestScore := math.Inf(-1)
	bestFeature := -1
	bestThreshold := -1
	bestMissingLeft := true
	minLeaf := float64(t.opts.MinLeaf)
	l2 := t.opts.L2
	for f := 0; f < t.nFeatures; f++ {
		thresholds := t.thresholds[f]
		if len(thresholds) == 0 {
			continue
		}
		valueBins := len(thresholds) + 1
		valueCount := 0.0
		valueSum := 0.0
		for b := 0; b < valueBins; b++ {
			valueCount += t.histC[f][b]
			valueSum += t.histS[f][b]
		}
		missingCount := t.histC[f][t.missingBin]
		missingSum := t.histS[f][t.missingBin]
		leftCount := 0.0
		leftSum := 0.0
		for j := 0; j < len(thresholds); j++ {
			leftCount += t.histC[f][j]
			leftSum += t.histS[f][j]
			rightCount := valueCount - leftCount
			rightSum := valueSum - leftSum
			if leftCount+missingCount >= minLeaf && rightCount >= minLeaf {
				score := splitScore(leftSum+missingSum, leftCount+missingCount, rightSum, rightCount, l2)
				if score > bestScore+1e-12 {
					bestScore, bestFeature, bestThreshold, bestMissingLeft = score, f, j, true
				}
			}
			if leftCount >= minLeaf && rightCount+missingCount >= minLeaf {
				score := splitScore(leftSum, leftCount, rightSum+missingSum, rightCount+missingCount, l2)
				if score > bestScore+1e-12 {
					bestScore, bestFeature, bestThreshold, bestMissingLeft = score, f, j, false
				}
			}
		}
	}
	if bestFeature < 0 {
		t.tree.Nodes[nodeIndex] = Node{Leaf: true, Value: leafValue(totalSum, totalCount, t.opts.L2)}
		return nodeIndex
	}
	left := make([]int32, 0, len(indices))
	right := make([]int32, 0, len(indices))
	for _, row := range indices {
		bin := int(t.binned[int(row)*t.nFeatures+bestFeature])
		switch {
		case bin == int(missingBin):
			if bestMissingLeft {
				left = append(left, row)
			} else {
				right = append(right, row)
			}
		case bin <= bestThreshold:
			left = append(left, row)
		default:
			right = append(right, row)
		}
	}
	if len(left) == 0 || len(right) == 0 {
		t.tree.Nodes[nodeIndex] = Node{Leaf: true, Value: leafValue(totalSum, totalCount, t.opts.L2)}
		return nodeIndex
	}
	leftNode := t.build(left, depth+1)
	rightNode := t.build(right, depth+1)
	t.tree.Nodes[nodeIndex] = Node{
		Feature:     bestFeature,
		Threshold:   t.thresholds[bestFeature][bestThreshold],
		Left:        leftNode,
		Right:       rightNode,
		MissingLeft: bestMissingLeft,
	}
	return nodeIndex
}

func leafValue(sum, count, l2 float64) float64 {
	if count == 0 {
		return 0
	}
	return sum / (count + l2)
}

func splitScore(leftSum, leftCount, rightSum, rightCount, l2 float64) float64 {
	left := 0.0
	right := 0.0
	if leftCount > 0 {
		left = leftSum * leftSum / (leftCount + l2)
	}
	if rightCount > 0 {
		right = rightSum * rightSum / (rightCount + l2)
	}
	return left + right
}

func percentile90(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copied := append([]float64(nil), values...)
	sort.Float64s(copied)
	index := int(math.Ceil(0.9*float64(len(copied)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copied) {
		index = len(copied) - 1
	}
	return copied[index]
}
