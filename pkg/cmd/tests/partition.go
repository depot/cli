package tests

import (
	"fmt"
	"os"
	"sort"
)

type splitMode string

const (
	splitModeTimings  splitMode = "timings"
	splitModeName     splitMode = "name"
	splitModeFileSize splitMode = "filesize"
)

type weightedCandidate struct {
	inputIndex int
	candidate  string
	weight     int64
}

type weightedShard struct {
	index  int
	weight int64
	count  int
	items  []weightedCandidate
}

func parseSplitMode(value string) (splitMode, error) {
	switch value {
	case "", string(splitModeTimings):
		return splitModeTimings, nil
	case string(splitModeName):
		return splitModeName, nil
	case string(splitModeFileSize):
		return splitModeFileSize, nil
	default:
		return "", fmt.Errorf("unknown split mode %q. Requires timings, name, or filesize", value)
	}
}

func validateShard(index, total int) error {
	if total <= 0 {
		return fmt.Errorf("--total must be greater than 0")
	}
	if index < 0 {
		return fmt.Errorf("--index must be greater than or equal to 0")
	}
	if index >= total {
		return fmt.Errorf("--index must be less than --total")
	}
	return nil
}

func partitionCandidates(candidates []string, mode splitMode, index, total int) ([]string, error) {
	if err := validateShard(index, total); err != nil {
		return nil, err
	}
	if total == 1 {
		return append([]string(nil), candidates...), nil
	}

	switch mode {
	case splitModeName:
		return assignWeightedCandidates(candidates, equalWeights(candidates), index, total), nil
	case splitModeFileSize:
		weights, err := fileSizeWeights(candidates)
		if err != nil {
			return nil, err
		}
		return assignWeightedCandidates(candidates, weights, index, total), nil
	default:
		return nil, fmt.Errorf("split mode %q must use the API", mode)
	}
}

func equalWeights(candidates []string) map[int]int64 {
	weights := make(map[int]int64, len(candidates))
	for i := range candidates {
		weights[i] = 1
	}
	return weights
}

func fileSizeWeights(candidates []string) (map[int]int64, error) {
	weights := make(map[int]int64, len(candidates))
	for i, candidate := range candidates {
		stat, err := os.Stat(candidate)
		if err != nil {
			return nil, fmt.Errorf("failed to stat candidate %q: %w", candidate, err)
		}
		if !stat.Mode().IsRegular() {
			return nil, fmt.Errorf("candidate %q is not a regular file", candidate)
		}
		weights[i] = stat.Size()
	}
	return weights, nil
}

func assignWeightedCandidates(candidates []string, weights map[int]int64, index, total int) []string {
	weighted := make([]weightedCandidate, 0, len(candidates))
	for i, candidate := range candidates {
		weighted = append(weighted, weightedCandidate{
			inputIndex: i,
			candidate:  candidate,
			weight:     weights[i],
		})
	}
	sort.SliceStable(weighted, func(i, j int) bool {
		if weighted[i].weight != weighted[j].weight {
			return weighted[i].weight > weighted[j].weight
		}
		if weighted[i].candidate != weighted[j].candidate {
			return weighted[i].candidate < weighted[j].candidate
		}
		return weighted[i].inputIndex < weighted[j].inputIndex
	})

	shards := make([]weightedShard, total)
	for i := range shards {
		shards[i].index = i
	}
	for _, candidate := range weighted {
		lightest := 0
		for i := 1; i < len(shards); i++ {
			if compareShardLoad(shards[i], shards[lightest]) < 0 {
				lightest = i
			}
		}
		shards[lightest].items = append(shards[lightest].items, candidate)
		shards[lightest].weight += candidate.weight
		shards[lightest].count++
	}

	selected := shards[index].items
	sort.SliceStable(selected, func(i, j int) bool {
		return selected[i].inputIndex < selected[j].inputIndex
	})

	result := make([]string, 0, len(selected))
	for _, candidate := range selected {
		result = append(result, candidate.candidate)
	}
	return result
}

func compareShardLoad(left, right weightedShard) int {
	if left.weight < right.weight {
		return -1
	}
	if left.weight > right.weight {
		return 1
	}
	if left.count < right.count {
		return -1
	}
	if left.count > right.count {
		return 1
	}
	if left.index < right.index {
		return -1
	}
	if left.index > right.index {
		return 1
	}
	return 0
}
