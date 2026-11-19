package watchtogether

import (
	"context"
)

type recordingCreator struct {
	stubSuggestions
	input        Suggestion
	calls, lists int
	listErr      error
}

func (r *recordingCreator) CreateSuggestionOnce(_ context.Context, input Suggestion) (*Suggestion, bool, error) {
	r.calls++
	r.input = input
	return &input, r.calls == 1, nil
}
func (r *recordingCreator) ListSuggestions(context.Context, string, int, string) ([]Suggestion, error) {
	r.lists++
	return []Suggestion{r.input}, r.listErr
}
