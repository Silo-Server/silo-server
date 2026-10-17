package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/workersource"
)

// Reader limits are host resource budgets. Read them once at startup rather
// than sharing one capacity value between API replicas with different hosts.
func workerSourceLimitsFromEnvironment() (workersource.Limits, error) {
	const maximum = workersource.MaximumReaders
	readers := workersource.DefaultMaxReaders
	if raw := os.Getenv("SILO_WORKER_SOURCE_MAX_READERS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximum {
			return workersource.Limits{}, fmt.Errorf("SILO_WORKER_SOURCE_MAX_READERS must be between 1 and %d", maximum)
		}
		readers = value
	}
	return workersource.Limits{MaxReaders: readers}, nil
}
