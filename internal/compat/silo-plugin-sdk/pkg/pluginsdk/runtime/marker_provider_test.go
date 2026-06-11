package runtime_test

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	runtime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
	"google.golang.org/grpc"
)

type stubMarkerProvider struct{}

func (stubMarkerProvider) FetchMarkers(context.Context, *pluginv1.FetchMarkersRequest) (*pluginv1.FetchMarkersResponse, error) {
	return &pluginv1.FetchMarkersResponse{}, nil
}

func (stubMarkerProvider) SubmitMarker(context.Context, *pluginv1.SubmitMarkerRequest) (*pluginv1.SubmitMarkerResponse, error) {
	return &pluginv1.SubmitMarkerResponse{}, nil
}

func (stubMarkerProvider) GetMarkerProviderStats(context.Context, *pluginv1.GetMarkerProviderStatsRequest) (*pluginv1.MarkerProviderStatsResponse, error) {
	return &pluginv1.MarkerProviderStatsResponse{}, nil
}

func TestGRPCServerRegistersMarkerProvider(t *testing.T) {
	p := &runtime.GRPCPlugin{Servers: runtime.CapabilityServers{
		Runtime:        stubRuntime{},
		MarkerProvider: stubMarkerProvider{},
	}}
	srv := grpc.NewServer()
	if err := p.GRPCServer(nil, srv); err != nil {
		t.Fatalf("GRPCServer with MarkerProvider = %v, want nil", err)
	}
	if _, ok := srv.GetServiceInfo()["silo.plugin.v1.MarkerProvider"]; !ok {
		t.Fatalf("MarkerProvider service not registered; got %v", srv.GetServiceInfo())
	}
}
