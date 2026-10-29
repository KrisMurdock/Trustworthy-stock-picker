package marketforecast

import (
	"math"
	"sort"
)

// sessionTree is a deterministic CART classifier. Splits are chosen by
// weighted Gini impurity; ties prefer the lower feature index and then the
// lower threshold. Leaf probabilities use a Beta(2,2) prior so a pure leaf
// never reports 0 or 1 from a small sample.
type sessionTree struct {
	root     *sessionTreeNode
	maxDepth int
	minLeaf  int
}

type sessionTreeNode struct {
	leaf        bool
	probability float64
	feature     int
	threshold   float64
	left        *sessionTreeNode
	right       *sessionTreeNode
}

func fitSessionTree(samples []sessionSample, maxDepth, minLeaf int) *sessionTree {
	if maxDepth < 0 {
		maxDepth = 0
	}
	if minLeaf < 1 {
		minLeaf = 1
	}
	tree := &sessionTree{maxDepth: maxDepth, minLeaf: minLeaf}
	if len(samples) == 0 {
		tree.root = &sessionTreeNode{leaf: true, probability: 0.5}
		return tree
	}
	indices := make([]int, len(samples))
	for index := range indices {
		indices[index] = index
	}
	tree.root = tree.build(samples, indices, 0)
	return tree
}

func (t *sessionTree) build(samples []sessionSample, indices []int, depth int) *sessionTreeNode {
	up := 0
	for _, index := range indices {
		if samples[index].label == 1 {
			up++
		}
	}
	node := &sessionTreeNode{leaf: true, probability: (float64(up) + 2) / (float64(len(indices)) + 4)}
	if len(indices) < 2*t.minLeaf || depth >= t.maxDepth || up == 0 || up == len(indices) {
		return node
	}
	features := len(samples[indices[0]].features)
	if features == 0 {
		return node
	}
	bestFeature := -1
	bestThreshold := 0.0
	bestScore := math.Inf(1)
	for feature := 0; feature < features; feature++ {
		values := make([]float64, 0, len(indices))
		for _, index := range indices {
			value := samples[index].features[feature]
			if !finite(value) {
				return node
			}
			values = append(values, value)
		}
		sort.Float64s(values)
		for position := 1; position < len(values); position++ {
			if values[position] == values[position-1] {
				continue
			}
			threshold := (values[position-1] + values[position]) / 2
			leftUp, leftCount, rightUp, rightCount := 0, 0, 0, 0
			for _, index := range indices {
				if samples[index].features[feature] <= threshold {
					leftCount++
					if samples[index].label == 1 {
						leftUp++
					}
				} else {
					rightCount++
					if samples[index].label == 1 {
						rightUp++
					}
				}
			}
			if leftCount < t.minLeaf || rightCount < t.minLeaf {
				continue
			}
			score := giniImpurity(leftUp, leftCount)*float64(leftCount) + giniImpurity(rightUp, rightCount)*float64(rightCount)
			if score < bestScore-1e-12 || (math.Abs(score-bestScore) <= 1e-12 && (bestFeature < 0 || feature < bestFeature || (feature == bestFeature && threshold < bestThreshold))) {
				bestScore, bestFeature, bestThreshold = score, feature, threshold
			}
		}
	}
	if bestFeature < 0 {
		return node
	}
	left := make([]int, 0, len(indices))
	right := make([]int, 0, len(indices))
	for _, index := range indices {
		if samples[index].features[bestFeature] <= bestThreshold {
			left = append(left, index)
		} else {
			right = append(right, index)
		}
	}
	if len(left) < t.minLeaf || len(right) < t.minLeaf {
		return node
	}
	node.leaf = false
	node.feature = bestFeature
	node.threshold = bestThreshold
	node.left = t.build(samples, left, depth+1)
	node.right = t.build(samples, right, depth+1)
	return node
}

func giniImpurity(up, count int) float64 {
	if count == 0 {
		return 0
	}
	p := float64(up) / float64(count)
	return 2 * p * (1 - p)
}

func (t *sessionTree) probability(features []float64) float64 {
	if t == nil || t.root == nil {
		return 0.5
	}
	node := t.root
	for !node.leaf {
		if node.feature < 0 || node.feature >= len(features) || node.left == nil || node.right == nil {
			break
		}
		if features[node.feature] <= node.threshold {
			node = node.left
		} else {
			node = node.right
		}
	}
	return node.probability
}
