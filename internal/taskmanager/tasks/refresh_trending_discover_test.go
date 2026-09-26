package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type stubTrendingRefresher struct {
	data json.RawMessage
	err  error
}

func (s stubTrendingRefresher) RunOnce(context.Context) (json.RawMessage, error) {
	return s.data, s.err
}

type trendingTaskProgress struct{ result json.RawMessage }

func (*trendingTaskProgress) Report(float64, string)               {}
func (p *trendingTaskProgress) SetResultData(data json.RawMessage) { p.result = data }

func TestRefreshTrendingDiscoverKeepsSummaryOnError(t *testing.T) {
	summary := json.RawMessage(`{"combos":1,"refreshed":1}`)
	task := NewRefreshTrendingDiscoverTask(stubTrendingRefresher{data: summary, err: errors.New("listing failed")})
	progress := &trendingTaskProgress{}

	if err := task.Execute(context.Background(), progress); err == nil {
		t.Fatal("Execute err = nil; want the listing error")
	}
	if string(progress.result) != string(summary) {
		t.Fatalf("result data = %s; want %s", progress.result, summary)
	}
}
