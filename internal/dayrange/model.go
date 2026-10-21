package dayrange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

const ModelSchemaVersion = 1
const modelKind = "gbdt_conformal"

var quantileLevels = []float64{.1, .5, .9}

type Hyperparameters struct {
	Trees        int     `json:"trees"`
	MaxDepth     int     `json:"max_depth"`
	MinLeaf      int     `json:"min_leaf"`
	LearningRate float64 `json:"learning_rate"`
	L2           float64 `json:"l2"`
	Bins         int     `json:"bins"`
	Seed         int64   `json:"seed"`
}

type TrainOptions struct {
	Hyperparameters
	LabelClip float64
}

func DefaultTrainOptions() TrainOptions {
	return TrainOptions{Hyperparameters: Hyperparameters{Trees: 80, MaxDepth: 4, MinLeaf: 40, LearningRate: .05, L2: 1, Bins: 32, Seed: 1949}, LabelClip: .5}
}

type Node struct {
	Feature     int     `json:"feature"`
	Threshold   float64 `json:"threshold"`
	Left        int     `json:"left"`
	Right       int     `json:"right"`
	MissingLeft bool    `json:"missing_left"`
	Leaf        bool    `json:"leaf"`
	Value       float64 `json:"value"`
}

type Tree struct {
	Nodes []Node `json:"nodes"`
}

type ModelInfo struct {
	SchemaVersion   int       `json:"schema_version"`
	CodeVersion     string    `json:"code_version"`
	CreatedAt       time.Time `json:"created_at"`
	TrainFrom       string    `json:"train_from"`
	TrainTo         string    `json:"train_to"`
	CalibrationFrom string    `json:"calibration_from"`
	CalibrationTo   string    `json:"calibration_to"`
	ValidationFrom  string    `json:"validation_from"`
	ValidationTo    string    `json:"validation_to"`
	ValidFor        string    `json:"valid_for"`
}

type Calibration struct {
	Method     string             `json:"method"`
	Window     int                `json:"window"`
	Levels     []float64          `json:"levels"`
	HighOffset map[string]float64 `json:"high_offset"`
	LowOffset  map[string]float64 `json:"low_offset"`
}

type Artifact struct {
	SchemaVersion   int                `json:"schema_version"`
	ModelID         string             `json:"model_id"`
	Kind            string             `json:"kind"`
	Info            ModelInfo          `json:"info"`
	FeatureNames    []string           `json:"feature_names"`
	Hyperparameters Hyperparameters    `json:"hyperparameters"`
	Base            map[string]float64 `json:"base"`
	Trees           map[string][]Tree  `json:"trees"`
	Calibration     Calibration        `json:"calibration"`
	Checksum        string             `json:"checksum"`
}

type Model struct{ Artifact Artifact }

func normalizeOptions(opts TrainOptions) TrainOptions {
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
	if opts.LearningRate <= 0 || opts.LearningRate > 1 {
		opts.LearningRate = defaults.LearningRate
	}
	if opts.L2 < 0 {
		opts.L2 = 0
	}
	if opts.Bins < 2 || opts.Bins > 64 {
		opts.Bins = defaults.Bins
	}
	if opts.Seed == 0 {
		opts.Seed = defaults.Seed
	}
	if opts.LabelClip <= 0 {
		opts.LabelClip = defaults.LabelClip
	}
	return opts
}

func TrainPointModel(ctx context.Context, samples []Sample, info ModelInfo, opts TrainOptions) (*Artifact, error) {
	opts = normalizeOptions(opts)
	ordered := append([]Sample(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Snapshot.TargetDate != ordered[j].Snapshot.TargetDate {
			return ordered[i].Snapshot.TargetDate < ordered[j].Snapshot.TargetDate
		}
		return ordered[i].Snapshot.Code < ordered[j].Snapshot.Code
	})
	if len(ordered) == 0 {
		return nil, errors.New("dayrange has no training samples")
	}
	names := FeatureNames()
	features := make([][]float64, len(ordered))
	for i, sample := range ordered {
		values := sample.Snapshot.Values()
		if len(values) != len(names) {
			return nil, errors.New("dayrange sample has an invalid feature count")
		}
		features[i] = values
	}
	binner := newBinner(features, len(names), opts.Bins)
	artifact := &Artifact{SchemaVersion: ModelSchemaVersion, Kind: modelKind, Info: info, FeatureNames: names,
		Hyperparameters: opts.Hyperparameters, Base: map[string]float64{}, Trees: map[string][]Tree{}}
	for _, target := range []string{"high", "low"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		labels := make([]float64, len(ordered))
		for i, sample := range ordered {
			if sample.Actual == nil || sample.Snapshot.ReferencePrice <= 0 {
				return nil, errors.New("dayrange training sample is unresolved")
			}
			value := math.Log(sample.Actual.High / sample.Snapshot.ReferencePrice)
			if target == "low" {
				value = math.Log(sample.Actual.Low / sample.Snapshot.ReferencePrice)
			}
			if !finite(value) {
				return nil, errors.New("dayrange training label is not finite")
			}
			labels[i] = clip(value, opts.LabelClip)
		}
		base := mean(labels)
		residual := make([]float64, len(labels))
		for i := range labels {
			residual[i] = labels[i] - base
		}
		trainer := &treeTrainer{residual: residual, features: features, binned: binner.data, nFeatures: len(names),
			opts: opts, thresholds: binner.thresholds, histC: make([][]float64, len(names)), histS: make([][]float64, len(names)), missingBin: opts.Bins + 1}
		for f := range trainer.histC {
			trainer.histC[f] = make([]float64, opts.Bins+2)
			trainer.histS[f] = make([]float64, opts.Bins+2)
		}
		trees := make([]Tree, 0, opts.Trees)
		for step := 0; step < opts.Trees; step++ {
			if step%8 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			tree := trainer.fit()
			trees = append(trees, tree)
			for i := range features {
				residual[i] -= opts.LearningRate * predictTree(tree, features[i])
			}
		}
		artifact.Base[target] = base
		artifact.Trees[target] = trees
	}
	return artifact, nil
}

func Calibrate(artifact *Artifact, samples []Sample) error {
	if artifact == nil {
		return errors.New("dayrange artifact is missing")
	}
	if len(samples) < 10 {
		return errors.New("dayrange calibration needs at least 10 samples")
	}
	residuals := map[string][]float64{"high": {}, "low": {}}
	for _, sample := range samples {
		if sample.Actual == nil || sample.Snapshot.ReferencePrice <= 0 {
			continue
		}
		high, low, err := predictRaw(artifact, sample.Snapshot.Values())
		if err != nil {
			return err
		}
		residuals["high"] = append(residuals["high"], math.Log(sample.Actual.High/sample.Snapshot.ReferencePrice)-high)
		residuals["low"] = append(residuals["low"], math.Log(sample.Actual.Low/sample.Snapshot.ReferencePrice)-low)
	}
	if len(residuals["high"]) < 10 || len(residuals["low"]) < 10 {
		return errors.New("dayrange calibration has insufficient resolved samples")
	}
	artifact.Calibration = Calibration{Method: "rolling_conformal_residual", Window: len(residuals["high"]), Levels: append([]float64(nil), quantileLevels...), HighOffset: map[string]float64{}, LowOffset: map[string]float64{}}
	for _, level := range quantileLevels {
		key := number(level)
		artifact.Calibration.HighOffset[key] = quantile(residuals["high"], level)
		artifact.Calibration.LowOffset[key] = quantile(residuals["low"], level)
	}
	return nil
}

func SealArtifact(artifact *Artifact) error {
	if artifact == nil {
		return errors.New("dayrange artifact is missing")
	}
	artifact.ModelID = ""
	artifact.Checksum = ""
	raw, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	first := sha256.Sum256(raw)
	artifact.ModelID = hex.EncodeToString(first[:])
	artifact.Checksum = ""
	raw, err = json.Marshal(artifact)
	if err != nil {
		return err
	}
	second := sha256.Sum256(raw)
	artifact.Checksum = hex.EncodeToString(second[:])
	return nil
}

func MarshalArtifact(artifact *Artifact) ([]byte, error) {
	if artifact == nil {
		return nil, errors.New("dayrange artifact is missing")
	}
	return json.MarshalIndent(artifact, "", "  ")
}

func LoadArtifact(data []byte) (*Model, error) {
	var artifact Artifact
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return nil, errors.New("invalid dayrange model artifact")
	}
	if artifact.SchemaVersion != ModelSchemaVersion || artifact.Kind != modelKind || artifact.ModelID == "" || len(artifact.FeatureNames) != len(featureNames) {
		return nil, errors.New("unsupported dayrange model artifact")
	}
	for i, name := range featureNames {
		if artifact.FeatureNames[i] != name {
			return nil, errors.New("dayrange feature schema mismatch")
		}
	}
	if len(artifact.Base) != 2 || len(artifact.Trees) != 2 || len(artifact.Calibration.Levels) != len(quantileLevels) {
		return nil, errors.New("dayrange model artifact is incomplete")
	}
	for _, target := range []string{"high", "low"} {
		if _, ok := artifact.Base[target]; !ok {
			return nil, errors.New("dayrange model artifact is missing a base")
		}
		trees := artifact.Trees[target]
		if len(trees) == 0 {
			return nil, errors.New("dayrange model artifact has no trees")
		}
		for _, tree := range trees {
			if err := validateTree(tree); err != nil {
				return nil, err
			}
		}
	}
	expected := artifact.Checksum
	artifact.Checksum = ""
	raw, err := json.Marshal(artifact)
	if err != nil {
		return nil, errors.New("cannot canonicalize dayrange artifact")
	}
	sum := sha256.Sum256(raw)
	if expected == "" || expected != hex.EncodeToString(sum[:]) {
		return nil, errors.New("dayrange model checksum mismatch")
	}
	artifact.Checksum = expected
	return &Model{Artifact: artifact}, nil
}

func validateTree(tree Tree) error {
	if len(tree.Nodes) == 0 || len(tree.Nodes) > 4096 {
		return errors.New("invalid dayrange tree size")
	}
	for i, node := range tree.Nodes {
		if node.Leaf {
			if !finite(node.Value) {
				return errors.New("invalid dayrange leaf value")
			}
			continue
		}
		if node.Feature < 0 || node.Feature >= len(featureNames) || node.Left < 0 || node.Right < 0 || node.Left >= len(tree.Nodes) || node.Right >= len(tree.Nodes) || node.Left == i || node.Right == i || !finite(node.Threshold) {
			return errors.New("invalid dayrange tree node")
		}
	}
	return nil
}

func (m *Model) Predict(values []float64, reference float64) (Forecast, error) {
	if m == nil {
		return Forecast{}, errors.New("dayrange model is not loaded")
	}
	if len(values) != len(m.Artifact.FeatureNames) || !positive(reference) {
		return Forecast{}, errors.New("dayrange prediction input is invalid")
	}
	highLog, lowLog, err := predictRaw(&m.Artifact, values)
	if err != nil {
		return Forecast{}, err
	}
	high, err := calibratedEstimate(reference, highLog, m.Artifact.Calibration.HighOffset, m.Artifact.Calibration.Method)
	if err != nil {
		return Forecast{}, err
	}
	low, err := calibratedEstimate(reference, lowLog, m.Artifact.Calibration.LowOffset, m.Artifact.Calibration.Method)
	if err != nil {
		return Forecast{}, err
	}
	if high.Point < low.Point {
		high.Point = low.Point
		high.Quantiles[1].Price = low.Quantiles[1].Price
	}
	for i := range high.Quantiles {
		if high.Quantiles[i].Price < low.Quantiles[i].Price {
			high.Quantiles[i].Price = low.Quantiles[i].Price
			high.Interval.Lower = math.Min(high.Interval.Lower, low.Interval.Lower)
			high.Interval.Upper = math.Max(high.Interval.Upper, low.Interval.Upper)
		}
	}
	high.Interval.Lower = math.Min(high.Interval.Lower, high.Point)
	high.Interval.Upper = math.Max(high.Interval.Upper, high.Point)
	low.Interval.Lower = math.Min(low.Interval.Lower, low.Point)
	low.Interval.Upper = math.Max(low.Interval.Upper, low.Point)
	return Forecast{High: high, Low: low}, nil
}

func calibratedEstimate(reference, pointLog float64, offsets map[string]float64, method string) (Estimate, error) {
	prices := make([]float64, 0, len(quantileLevels))
	for _, level := range quantileLevels {
		offset, ok := offsets[number(level)]
		if !ok || !finite(offset) {
			return Estimate{}, errors.New("dayrange calibration is incomplete")
		}
		price := reference * math.Exp(pointLog+offset)
		if !positive(price) || !finite(price) {
			return Estimate{}, errors.New("dayrange produced an invalid price")
		}
		prices = append(prices, price)
	}
	for i := 1; i < len(prices); i++ {
		if prices[i] < prices[i-1] {
			prices[i] = prices[i-1]
		}
	}
	quantiles := make([]Quantile, len(quantileLevels))
	for i, level := range quantileLevels {
		quantiles[i] = Quantile{Level: level, Price: prices[i]}
	}
	return Estimate{Point: prices[1], Quantiles: quantiles, Interval: &Interval{Lower: prices[0], Upper: prices[2], NominalCoverage: .8, Method: method}}, nil
}

func predictRaw(artifact *Artifact, values []float64) (float64, float64, error) {
	if artifact == nil || len(values) != len(artifact.FeatureNames) {
		return 0, 0, errors.New("dayrange feature count mismatch")
	}
	out := map[string]float64{}
	for _, target := range []string{"high", "low"} {
		sum := artifact.Base[target]
		for _, tree := range artifact.Trees[target] {
			sum += artifact.Hyperparameters.LearningRate * predictTree(tree, values)
		}
		if !finite(sum) {
			return 0, 0, errors.New("dayrange model produced a non-finite value")
		}
		out[target] = sum
	}
	return out["high"], out["low"], nil
}

func predictTree(tree Tree, values []float64) float64 {
	index := 0
	for {
		node := tree.Nodes[index]
		if node.Leaf {
			return node.Value
		}
		value := values[node.Feature]
		if math.IsNaN(value) {
			if node.MissingLeft {
				index = node.Left
			} else {
				index = node.Right
			}
			continue
		}
		if value <= node.Threshold {
			index = node.Left
		} else {
			index = node.Right
		}
	}
}

type binner struct {
	thresholds [][]float64
	data       []uint8
}

func newBinner(features [][]float64, nFeatures, maxBins int) *binner {
	out := &binner{thresholds: make([][]float64, nFeatures), data: make([]uint8, len(features)*nFeatures)}
	for f := 0; f < nFeatures; f++ {
		values := make([]float64, 0, len(features))
		for _, row := range features {
			if finite(row[f]) {
				values = append(values, row[f])
			}
		}
		out.thresholds[f] = featureThresholds(values, maxBins)
		for i, row := range features {
			value := row[f]
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

const missingBin = uint8(255)

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
		index := k * len(unique) / maxBins
		if index <= 0 || index >= len(unique) {
			continue
		}
		threshold := unique[index-1]
		if len(out) == 0 || threshold > out[len(out)-1] {
			out = append(out, threshold)
		}
	}
	return out
}

type treeTrainer struct {
	residual   []float64
	features   [][]float64
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
	totalSum := 0.0
	for _, row := range indices {
		totalSum += t.residual[row]
	}
	if depth >= t.opts.MaxDepth || len(indices) < 2*t.opts.MinLeaf {
		t.tree.Nodes[nodeIndex] = Node{Leaf: true, Value: leafValue(totalSum, float64(len(indices)), t.opts.L2)}
		return nodeIndex
	}
	for f := 0; f < t.nFeatures; f++ {
		for b := 0; b <= t.opts.Bins+1; b++ {
			t.histC[f][b], t.histS[f][b] = 0, 0
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
	best := math.Inf(-1)
	bestFeature, bestThreshold := -1, -1
	bestMissingLeft := true
	minLeaf := float64(t.opts.MinLeaf)
	for f := 0; f < t.nFeatures; f++ {
		thresholds := t.thresholds[f]
		if len(thresholds) == 0 {
			continue
		}
		valueCount, valueSum := 0.0, 0.0
		for b := 0; b < len(thresholds)+1; b++ {
			valueCount += t.histC[f][b]
			valueSum += t.histS[f][b]
		}
		missingCount, missingSum := t.histC[f][t.missingBin], t.histS[f][t.missingBin]
		leftCount, leftSum := 0.0, 0.0
		for j := range thresholds {
			leftCount += t.histC[f][j]
			leftSum += t.histS[f][j]
			rightCount, rightSum := valueCount-leftCount, valueSum-leftSum
			if leftCount+missingCount >= minLeaf && rightCount >= minLeaf {
				score := splitScore(leftSum+missingSum, leftCount+missingCount, rightSum, rightCount, t.opts.L2)
				if score > best+1e-12 {
					best, bestFeature, bestThreshold, bestMissingLeft = score, f, j, true
				}
			}
			if leftCount >= minLeaf && rightCount+missingCount >= minLeaf {
				score := splitScore(leftSum, leftCount, rightSum+missingSum, rightCount+missingCount, t.opts.L2)
				if score > best+1e-12 {
					best, bestFeature, bestThreshold, bestMissingLeft = score, f, j, false
				}
			}
		}
	}
	if bestFeature < 0 {
		t.tree.Nodes[nodeIndex] = Node{Leaf: true, Value: leafValue(totalSum, float64(len(indices)), t.opts.L2)}
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
		t.tree.Nodes[nodeIndex] = Node{Leaf: true, Value: leafValue(totalSum, float64(len(indices)), t.opts.L2)}
		return nodeIndex
	}
	leftNode := t.build(left, depth+1)
	rightNode := t.build(right, depth+1)
	t.tree.Nodes[nodeIndex] = Node{Feature: bestFeature, Threshold: t.thresholds[bestFeature][bestThreshold], Left: leftNode, Right: rightNode, MissingLeft: bestMissingLeft}
	return nodeIndex
}

func leafValue(sum, count, l2 float64) float64 {
	if count == 0 {
		return 0
	}
	return sum / (count + l2)
}

func splitScore(leftSum, leftCount, rightSum, rightCount, l2 float64) float64 {
	left, right := 0.0, 0.0
	if leftCount > 0 {
		left = leftSum * leftSum / (leftCount + l2)
	}
	if rightCount > 0 {
		right = rightSum * rightSum / (rightCount + l2)
	}
	return left + right
}

func clip(value, limit float64) float64 {
	if limit <= 0 {
		return value
	}
	return math.Max(-limit, math.Min(limit, value))
}

type Gate struct {
	Passed            bool     `json:"passed"`
	Reasons           []string `json:"reasons,omitempty"`
	HighMAE           float64  `json:"high_mae"`
	LowMAE            float64  `json:"low_mae"`
	HighCoverage      float64  `json:"high_coverage"`
	LowCoverage       float64  `json:"low_coverage"`
	HighWidthRatio    float64  `json:"high_width_ratio"`
	LowWidthRatio     float64  `json:"low_width_ratio"`
	JointCoverage     float64  `json:"joint_coverage"`
	ValidationDays    int      `json:"validation_days"`
	ValidationSamples int      `json:"validation_samples"`
}

// CheckGate applies the documented promotion rule. It is deliberately strict:
// a wider interval cannot buy its way past a point-forecast regression.
func CheckGate(candidate, baseline Report, minimumDays, minimumSamples int) Gate {
	gate := Gate{ValidationDays: candidate.Resolved, ValidationSamples: metricSamples(candidate, modeleval.MetricPointMAE, modeleval.TargetHigh)}
	gate.ValidationDays = metricDays(candidate, modeleval.MetricPointMAE, modeleval.TargetHigh)
	gate.Reasons = []string{}
	gate.JointCoverage, _ = metricValue(candidate, modeleval.MetricIntervalJointCoverage, modeleval.TargetAll, modeleval.AggregationDayEqual, modeleval.SpaceNone)
	candidateHighMAE, okHighMAE := metricValue(candidate, modeleval.MetricPointMAE, modeleval.TargetHigh, modeleval.AggregationDayEqual, modeleval.SpacePrice)
	candidateLowMAE, okLowMAE := metricValue(candidate, modeleval.MetricPointMAE, modeleval.TargetLow, modeleval.AggregationDayEqual, modeleval.SpacePrice)
	baselineHighMAE, okBaseHigh := metricValue(baseline, modeleval.MetricPointMAE, modeleval.TargetHigh, modeleval.AggregationDayEqual, modeleval.SpacePrice)
	baselineLowMAE, okBaseLow := metricValue(baseline, modeleval.MetricPointMAE, modeleval.TargetLow, modeleval.AggregationDayEqual, modeleval.SpacePrice)
	highCoverage, okHighCoverage := metricValue(candidate, modeleval.MetricIntervalEmpiricalCoverage, modeleval.TargetHigh, modeleval.AggregationDayEqual, modeleval.SpaceNone)
	lowCoverage, okLowCoverage := metricValue(candidate, modeleval.MetricIntervalEmpiricalCoverage, modeleval.TargetLow, modeleval.AggregationDayEqual, modeleval.SpaceNone)
	highWidth, okHighWidth := metricValue(candidate, modeleval.MetricIntervalWidthMean, modeleval.TargetHigh, modeleval.AggregationDayEqual, modeleval.SpacePrice)
	lowWidth, okLowWidth := metricValue(candidate, modeleval.MetricIntervalWidthMean, modeleval.TargetLow, modeleval.AggregationDayEqual, modeleval.SpacePrice)
	baseHighWidth, okBaseHighWidth := metricValue(baseline, modeleval.MetricIntervalWidthMean, modeleval.TargetHigh, modeleval.AggregationDayEqual, modeleval.SpacePrice)
	baseLowWidth, okBaseLowWidth := metricValue(baseline, modeleval.MetricIntervalWidthMean, modeleval.TargetLow, modeleval.AggregationDayEqual, modeleval.SpacePrice)
	gate.HighMAE, gate.LowMAE = candidateHighMAE, candidateLowMAE
	gate.HighCoverage, gate.LowCoverage = highCoverage, lowCoverage
	highWidthRatio, okHighWidthRatio := ratio(highWidth, baseHighWidth)
	lowWidthRatio, okLowWidthRatio := ratio(lowWidth, baseLowWidth)
	gate.HighWidthRatio, gate.LowWidthRatio = highWidthRatio, lowWidthRatio
	valid := okHighMAE && okLowMAE && okBaseHigh && okBaseLow && okHighCoverage && okLowCoverage && okHighWidth && okLowWidth && okBaseHighWidth && okBaseLowWidth && okHighWidthRatio && okLowWidthRatio
	if !valid {
		gate.Reasons = append(gate.Reasons, "missing_required_metrics")
	}
	if gate.ValidationDays < minimumDays || gate.ValidationSamples < minimumSamples {
		gate.Reasons = append(gate.Reasons, "insufficient_validation")
	}
	if valid {
		if candidateHighMAE > baselineHighMAE*.95 {
			gate.Reasons = append(gate.Reasons, "high_point_not_5pct_better")
		}
		if candidateLowMAE > baselineLowMAE*.95 {
			gate.Reasons = append(gate.Reasons, "low_point_not_5pct_better")
		}
		if highCoverage < .75 || highCoverage > .85 || lowCoverage < .75 || lowCoverage > .85 {
			gate.Reasons = append(gate.Reasons, "marginal_coverage_outside_75_85")
		}
		if gate.HighWidthRatio > 1.10 || gate.LowWidthRatio > 1.10 {
			gate.Reasons = append(gate.Reasons, "interval_width_over_110pct")
		}
	}
	gate.Passed = len(gate.Reasons) == 0
	return gate
}

func metricValue(report Report, id, target string, agg modeleval.Aggregation, space modeleval.Space) (float64, bool) {
	for _, metric := range report.Metrics {
		if metric.MetricID != id || metric.Target != target || metric.Aggregation != agg || metric.Space != space || metric.Value == nil {
			continue
		}
		if !baseSlice(metric.Slice) {
			continue
		}
		if metric.Applicability != modeleval.ApplicabilityOK {
			continue
		}
		return *metric.Value, true
	}
	return 0, false
}

func metricDays(report Report, id, target string) int {
	for _, metric := range report.Metrics {
		if metric.MetricID == id && metric.Target == target && metric.Aggregation == modeleval.AggregationDayEqual && metric.Space == modeleval.SpacePrice && baseSlice(metric.Slice) {
			return metric.Days
		}
	}
	return 0
}

func metricSamples(report Report, id, target string) int {
	for _, metric := range report.Metrics {
		if metric.MetricID == id && metric.Target == target && metric.Aggregation == modeleval.AggregationDayEqual && metric.Space == modeleval.SpacePrice && baseSlice(metric.Slice) {
			return metric.Samples
		}
	}
	return 0
}

func baseSlice(slice []modeleval.SliceDimension) bool {
	if len(slice) < 2 {
		return false
	}
	asOf, mode := false, false
	for _, dimension := range slice {
		switch dimension.Name {
		case "as_of":
			asOf = true
		case "mode":
			mode = true
		}
	}
	return asOf && mode
}

func ratio(value, base float64) (float64, bool) {
	if base <= 0 || !finite(value) || !finite(base) {
		return 0, false
	}
	return value / base, true
}

// EvaluateValidation creates observations for both the candidate and its
// matched baseline. The caller supplies only the chronological validation
// window; training and calibration samples must never be passed here.
func EvaluateValidation(ctx context.Context, samples []Sample, candidateID string, forecast func(Sample) (Forecast, error)) (Report, Report, error) {
	if len(samples) == 0 {
		return Report{}, Report{}, errors.New("dayrange validation is empty")
	}
	candidate := make([]Observation, 0, len(samples))
	baseline := make([]Observation, 0, len(samples))
	for _, sample := range samples {
		if sample.Actual == nil {
			continue
		}
		forecastValue, err := forecast(sample)
		if err != nil {
			return Report{}, Report{}, err
		}
		base := sample.Snapshot.Baseline
		if base == nil {
			continue
		}
		candidate = append(candidate, Observation{Code: sample.Snapshot.Code, Date: sample.Snapshot.TargetDate, Mode: ModePremarket, AsOf: sample.Snapshot.AsOf,
			ReferencePrice: sample.Snapshot.ReferencePrice, ScalePrice: sample.Snapshot.ScalePrice, Forecast: forecastValue, Actual: sample.Actual,
			Baseline: &Extremes{High: base.High.Point, Low: base.Low.Point}, Slices: sample.Snapshot.Slices})
		baseline = append(baseline, Observation{Code: sample.Snapshot.Code, Date: sample.Snapshot.TargetDate, Mode: ModePremarket, AsOf: sample.Snapshot.AsOf,
			ReferencePrice: sample.Snapshot.ReferencePrice, ScalePrice: sample.Snapshot.ScalePrice, Forecast: *base, Actual: sample.Actual, Slices: sample.Snapshot.Slices})
	}
	opts := Options{Subject: "model:" + candidateID, Baseline: BaselineModelID}
	candidateReport, err := Evaluate(ctx, candidate, opts)
	if err != nil {
		return Report{}, Report{}, err
	}
	baselineReport, err := Evaluate(ctx, baseline, Options{Subject: "baseline:" + BaselineModelID})
	if err != nil {
		return Report{}, Report{}, err
	}
	return candidateReport, baselineReport, nil
}

// ModelView is the redacted model metadata exposed by CLI/API.
type ModelView struct {
	ModelID         string             `json:"model_id"`
	Kind            string             `json:"kind"`
	Status          string             `json:"status"`
	FeatureVersion  int                `json:"feature_version"`
	CodeVersion     string             `json:"code_version,omitempty"`
	TrainFrom       string             `json:"train_from,omitempty"`
	TrainTo         string             `json:"train_to,omitempty"`
	CalibrationFrom string             `json:"calibration_from,omitempty"`
	CalibrationTo   string             `json:"calibration_to,omitempty"`
	ValidationFrom  string             `json:"validation_from,omitempty"`
	ValidationTo    string             `json:"validation_to,omitempty"`
	ValidFor        string             `json:"valid_for,omitempty"`
	Checksum        string             `json:"checksum,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	Reason          string             `json:"reason,omitempty"`
	MetricsSummary  map[string]float64 `json:"metrics_summary,omitempty"`
}

func (a Artifact) View() ModelView {
	return ModelView{ModelID: a.ModelID, Kind: a.Kind, Status: "research", FeatureVersion: FeatureVersion, CodeVersion: a.Info.CodeVersion,
		TrainFrom: a.Info.TrainFrom, TrainTo: a.Info.TrainTo, CalibrationFrom: a.Info.CalibrationFrom, CalibrationTo: a.Info.CalibrationTo,
		ValidationFrom: a.Info.ValidationFrom, ValidationTo: a.Info.ValidationTo, ValidFor: a.Info.ValidFor, Checksum: a.Checksum, CreatedAt: a.Info.CreatedAt}
}

func ValidateInfo(info ModelInfo) error {
	if strings.TrimSpace(info.CodeVersion) == "" || info.CreatedAt.IsZero() {
		return errors.New("dayrange model info is incomplete")
	}
	for _, value := range []string{info.TrainFrom, info.TrainTo, info.CalibrationFrom, info.CalibrationTo, info.ValidationFrom, info.ValidationTo, info.ValidFor} {
		if value == "" {
			return errors.New("dayrange model date range is incomplete")
		}
	}
	return nil
}
