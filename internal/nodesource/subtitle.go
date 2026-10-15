package nodesource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
)

// ErrSubtitlePending is the core's answer that the subtitle bundle asked for
// is still being built (202). The caller asks again.
var ErrSubtitlePending = errors.New("nodesource: the core is still building the subtitle bundle")

// FetchSubtitle asks the core once for the subtitle bundle of track, a
// subtitle stream ordinal of codec, of the handle's source. It returns the
// bundle's body for the caller to read and close, ErrSubtitlePending while the
// core builds it, or the core's other answer as a *StatusError: 422 when the
// source cannot give the bundle, a fatal status when the source is refused.
func (h *Handle) FetchSubtitle(ctx context.Context, track int, codec string) (io.ReadCloser, error) {
	if !h.remote {
		return nil, errors.New("nodesource: the source is read locally")
	}
	r := h.relay
	query := url.Values{QuerySubtitleTrack: {strconv.Itoa(track)}, QuerySubtitleCodec: {codec}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.coreURL+PathSubtitle+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(HeaderSourceGrant, h.currentGrant())
	req.Header.Set("Accept-Encoding", "identity")
	if err := nodeauth.SignToCore(req, r.coreURL, r.nodeID, r.key); err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return nil, ErrSubtitlePending
	}
	return nil, &StatusError{Op: opSubtitle, Status: resp.StatusCode}
}
