package scanner

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var scannedFiles = promauto.NewCounterVec(prometheus.CounterOpts{Name: "streamapp_scanner_files_total", Help: "Total number of files processed by the scanner."}, []string{"status"})

func observeFile(action fileAction, err error) {
	status := "error"
	if err == nil {
		switch action {
		case actionNew:
			status = "new"
		case actionUpdated:
			status = "updated"
		case actionUnchanged:
			status = "unchanged"
		}
	}
	scannedFiles.WithLabelValues(status).Inc()
}
