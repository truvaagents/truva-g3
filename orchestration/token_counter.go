package orchestration

import (
	"context"
	"math"
)

type HeuristicTokenCounter struct{}

func (HeuristicTokenCounter) CountTokens(_ context.Context, text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	return int(math.Ceil(float64(len(text)) / 3.5)), nil
}
