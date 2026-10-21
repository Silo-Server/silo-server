package transcodenode

import (
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
)

func writeEncodingCapacityError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, encodinglimit.ErrAtCapacity) {
		return false
	}
	w.Header().Set("Retry-After", "1")
	http.Error(w, "node is at encoding capacity; retry shortly", http.StatusServiceUnavailable)
	return true
}
