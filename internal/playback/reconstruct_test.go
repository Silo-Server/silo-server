package playback

import (
	"sync"
	"testing"
)

func TestRegisterReconstructedConcurrent(t *testing.T) {
	m := NewSessionManager(0, 0)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.RegisterReconstructed(&Session{ID: "race", UserID: 5})
		}()
	}
	wg.Wait()
	if _, err := m.GetSession("race"); err != nil {
		t.Fatalf("session should exist after concurrent registers: %v", err)
	}
}
