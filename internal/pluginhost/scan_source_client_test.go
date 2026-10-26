package pluginhost

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeScanSourceRPC struct {
	// waitForDeadline makes the call block until its context ends and
	// return the status grpc-go produces for that, as a timed-out call does.
	waitForDeadline bool
	err             error
}

func (f fakeScanSourceRPC) PollChanges(ctx context.Context, _ *pluginv1.PollChangesRequest, _ ...grpc.CallOption) (*pluginv1.PollChangesResponse, error) {
	if f.waitForDeadline {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return nil, f.err
}

func TestScanSourceClientPollChangesMarksHostDeadline(t *testing.T) {
	c := &ScanSourceClient{client: fakeScanSourceRPC{waitForDeadline: true}, timeout: time.Millisecond}
	_, err := c.PollChanges(context.Background(), &pluginv1.PollChangesRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.DeadlineExceeded {
		t.Fatalf("status code lost: %v", err)
	}
}

func TestScanSourceClientPollChangesKeepsPluginStatus(t *testing.T) {
	pluginErr := status.Error(codes.DeadlineExceeded, "history request to Sonarr timed out")
	c := &ScanSourceClient{client: fakeScanSourceRPC{err: pluginErr}, timeout: time.Minute}
	_, err := c.PollChanges(context.Background(), &pluginv1.PollChangesRequest{})
	if err != pluginErr { //nolint:errorlint // the plugin's error must come back unwrapped
		t.Fatalf("err = %v, want the plugin's status unchanged", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("plugin-chosen DeadlineExceeded must not read as a host timeout")
	}
}
