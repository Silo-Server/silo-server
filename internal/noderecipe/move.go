package noderecipe

import (
	"encoding/json"
	"errors"
	"strings"
)

// A transport whose recipe lives in this store can move to another transcode
// node without new client URLs. Central rewrites the record to name the new
// executor, and that node rebuilds the transcode from the record on its first
// request, the same way a restarted node does. A relay that cannot reach the
// node it was using reads the record to learn where the transport runs now.
//
// A proxy cannot move a transport itself: it holds no session state and does
// not plan routes. It publishes a MoveRequest (cache.EventTranscodeMoveRequested
// on cache.ChannelTranscodeMove) and follows the record once the API server that
// owns the session has rewritten it.

// MoveRequest asks central to move a transport off a transcode node that a
// relay could not reach.
type MoveRequest struct {
	// TransportID is the store key: the id the node serves the transport under.
	TransportID string `json:"transport_id"`
	// NodeURL is the transcode node the relay could not reach.
	NodeURL string `json:"node_url"`
	// FailedNodes are nodes the same relay request could not reach before
	// NodeURL. The API can still list them as healthy, so the move leaves them
	// out too.
	FailedNodes []string `json:"failed_nodes,omitempty"`
}

// EncodeMoveRequest renders a move request as an event payload.
func EncodeMoveRequest(request MoveRequest) (string, error) {
	if strings.TrimSpace(request.TransportID) == "" || strings.TrimSpace(request.NodeURL) == "" {
		return "", errors.New("move request needs a transport id and a node url")
	}
	data, _ := json.Marshal(request)
	return string(data), nil
}

// DecodeMoveRequest parses an event payload written by EncodeMoveRequest.
func DecodeMoveRequest(payload string) (MoveRequest, bool) {
	var request MoveRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return MoveRequest{}, false
	}
	if strings.TrimSpace(request.TransportID) == "" || strings.TrimSpace(request.NodeURL) == "" {
		return MoveRequest{}, false
	}
	return request, true
}
