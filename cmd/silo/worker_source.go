package main

import (
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

// workerSourceOwnerURL validates a startup-only direct address for this core
// process. A shared public/load-balancer URL cannot establish source ownership.
func workerSourceOwnerURL(raw, publicOrigin string) (string, error) {
	if raw == "" {
		return "", nil
	}
	origin, err := workeridentity.ValidateOrigin(strings.TrimRight(raw, "/"), true)
	if err != nil || origin == publicOrigin {
		return "", fmt.Errorf("SILO_WORKER_SOURCE_URL must be a direct core origin distinct from its public origin")
	}
	return origin, nil
}
