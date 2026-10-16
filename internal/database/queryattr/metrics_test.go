package queryattr

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestImportRegistersNoMetrics(t *testing.T) {
	for _, c := range []prometheus.Collector{queriesTotal, querySecondsTotal} {
		if err := prometheus.DefaultRegisterer.Register(c); err != nil {
			t.Errorf("collector already registered: %v", err)
		} else {
			t.Cleanup(func() { prometheus.DefaultRegisterer.Unregister(c) })
		}
	}
}
