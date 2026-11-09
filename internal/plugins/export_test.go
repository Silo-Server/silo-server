package plugins

import "github.com/Silo-Server/silo-server/internal/events"

// PublicPayloadJSON is what the dispatcher stores for env: the payload in its
// catalog shape. The external contract test runs producers through it.
func PublicPayloadJSON(env events.Envelope) ([]byte, error) {
	payload, err := publicPayload(env)
	if err != nil || payload == nil {
		return nil, err
	}
	return payload.MarshalJSON()
}
