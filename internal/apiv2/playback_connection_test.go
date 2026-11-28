package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

type connectionPlaybackFake struct {
	fakePlaybackService
	device string
	// receipt is the connection_receipt the last probe result carried; reportErr
	// is what a probe result report returns.
	receipt   string
	reportErr error
}

func (f *connectionPlaybackFake) ReportPlaybackProbe(_ context.Context, device string, result connectionstore.ProbeResult, receipt string) error {
	f.device, f.receipt = device, receipt
	if result.Kbps != 48000 || result.Bytes != 12000000 || result.DurationMS != 2000 || result.Transport != "wifi" {
		panic("unexpected measurement")
	}
	return f.reportErr
}

// A probe result carries its connection receipt to the service, and the
// service's two receipt refusals are 422 problems on body.connection_receipt
// with the codes missing and invalid.
func TestProbeResultsConnectionReceipt(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &connectionPlaybackFake{}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	headers := viewerHeaders()
	headers["X-Silo-Device-Id"] = "probe-device"
	path := Prefix + "/playback/bandwidth-probe/results"
	body := `{"kbps":48000,"bytes":12000000,"duration_ms":2000,"transport":"wifi","connection_receipt":"receipt-value"}`
	if res := do(t, h, http.MethodPost, path, body, headers); res.Code != 204 || fake.receipt != "receipt-value" {
		t.Fatalf("report %d %s receipt=%q", res.Code, res.Body.String(), fake.receipt)
	}
	for _, tc := range []struct {
		err  error
		code string
	}{{handlers.ErrProbeReceiptMissing, "missing"}, {handlers.ErrProbeReceiptInvalid, "invalid"}} {
		fake.reportErr = tc.err
		res := do(t, h, http.MethodPost, path, body, headers)
		requireProblem(t, res, TypeValidationFailed)
		if !strings.Contains(res.Body.String(), `"location":"body.connection_receipt"`) || !strings.Contains(res.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatalf("%v: body %s, want location body.connection_receipt and code %s", tc.err, res.Body.String(), tc.code)
		}
	}
	// A failed read of the device's enrollment is not a receipt refusal: the
	// result is neither recorded nor refused as invalid.
	fake.reportErr = errors.New("enrollment store unavailable")
	requireProblem(t, do(t, h, http.MethodPost, path, strings.Replace(body, `,"connection_receipt":"receipt-value"`, "", 1), headers), TypeDependencyUnavailable)
}
