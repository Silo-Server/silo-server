package requests

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type fakeRouterClient struct{ lastReq *pluginv1.FulfillRequest }

func (f *fakeRouterClient) Fulfill(_ context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error) {
	f.lastReq = req
	return &pluginv1.FulfillResponse{Targets: []*pluginv1.FulfillmentTarget{
		{Quality: "1080p", ConnectionId: "c1", ExternalId: "7", Status: "queued"},
	}}, nil
}
func (f *fakeRouterClient) CheckStatus(context.Context, *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error) {
	return &pluginv1.CheckStatusResponse{}, nil
}
func (f *fakeRouterClient) ListConfigOptions(context.Context, *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error) {
	return &pluginv1.ListConfigOptionsResponse{}, nil
}
func (f *fakeRouterClient) TestConnection(context.Context, *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error) {
	return &pluginv1.TestConnectionResponse{Ok: true}, nil
}

type fakeRouterResolver struct{ c RouterClient }

func (r fakeRouterResolver) RequestRouterClient(context.Context, int, string) (RouterClient, error) {
	return r.c, nil
}

func TestPluginRouterProviderFulfillTranslates(t *testing.T) {
	fc := &fakeRouterClient{}
	p := NewPluginRouterProvider(fakeRouterResolver{c: fc})
	year := 2020
	req := Request{MediaType: MediaTypeMovie, TMDBID: 42, Title: "X", Year: &year}
	targets, msg, err := p.Fulfill(context.Background(), 1, "arr", req, []Quality{Quality1080p},
		[]ResolvedRouterConnection{{ID: "c1", BaseURL: "http://r", APIKey: "k", Config: map[string]any{"service_kind": "radarr"}}})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if msg != "" || len(targets) != 1 || targets[0].Status != StatusQueued || targets[0].ExternalID != "7" {
		t.Fatalf("unexpected result: %+v msg=%q", targets, msg)
	}
	if fc.lastReq.GetRequest().GetExternalIds()["tmdb"] != "42" || len(fc.lastReq.GetQualities()) != 1 {
		t.Fatalf("descriptor/qualities not forwarded: %+v", fc.lastReq)
	}
}
