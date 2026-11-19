package nodecontrol

import (
	"context"
	"encoding/json"
	"sync"
)

type recordingPoster struct {
	mu      sync.Mutex
	reports []Report
	sizes   []int
}

func (p *recordingPoster) PostReport(_ context.Context, body []byte) error {
	var report Report
	if err := json.Unmarshal(body, &report); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reports = append(p.reports, report)
	p.sizes = append(p.sizes, len(body))
	return nil
}

func (p *recordingPoster) all() []Report {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Report(nil), p.reports...)
}
