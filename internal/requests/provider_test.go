package requests

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type fakeRouterClient struct {
	lastReq        *pluginv1.FulfillRequest
	lastCheckReq   *pluginv1.CheckStatusRequest
	statuses       []*pluginv1.TargetStatus
	optionsByField map[string]*pluginv1.ConfigOptionList
	validateResp   *pluginv1.ValidateResponse
}

func (f *fakeRouterClient) Fulfill(_ context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error) {
	f.lastReq = req
	return &pluginv1.FulfillResponse{Targets: []*pluginv1.FulfillmentTarget{
		{Quality: "1080p", ConnectionId: "c1", ExternalId: "7", Status: "queued"},
	}}, nil
}
func (f *fakeRouterClient) CheckStatus(_ context.Context, req *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error) {
	f.lastCheckReq = req
	return &pluginv1.CheckStatusResponse{Statuses: f.statuses}, nil
}
func (f *fakeRouterClient) ListConfigOptions(context.Context, *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error) {
	return &pluginv1.ListConfigOptionsResponse{OptionsByField: f.optionsByField}, nil
}
func (f *fakeRouterClient) TestConnection(context.Context, *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error) {
	return &pluginv1.TestConnectionResponse{Ok: true}, nil
}
func (f *fakeRouterClient) Validate(context.Context, *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error) {
	return f.validateResp, nil
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
	gotQ := fc.lastReq.GetQualities()
	if fc.lastReq.GetRequest().GetExternalIds()["tmdb"] != "42" || len(gotQ) != 1 {
		t.Fatalf("descriptor/qualities not forwarded: %+v", fc.lastReq)
	}
	if gotQ[0].GetId() != "1080p" || gotQ[0].GetIs4K() {
		t.Fatalf("1080p should be id=1080p is4k=false, got %+v", gotQ[0])
	}
}

func TestPluginRouterProviderCheckStatusTranslates(t *testing.T) {
	fc := &fakeRouterClient{statuses: []*pluginv1.TargetStatus{
		{Quality: "1080p", ConnectionId: "c1", Status: "downloading", ExternalStatus: "grabbed", Message: "in queue"},
		{Quality: "2160p", ConnectionId: "c2", Status: "completed", ExternalStatus: "imported", Message: ""},
	}}
	p := NewPluginRouterProvider(fakeRouterResolver{c: fc})
	req := Request{MediaType: MediaTypeMovie, TMDBID: 42, Title: "X"}
	refs := []RouterTargetRef{
		{Quality: Quality1080p, ConnectionID: "c1", ExternalID: "7"},
		{Quality: Quality2160p, ConnectionID: "c2", ExternalID: "8"},
	}
	out, err := p.CheckStatus(context.Background(), 1, "arr", req, refs,
		[]ResolvedRouterConnection{{ID: "c1", BaseURL: "http://r", APIKey: "k", Config: map[string]any{"service_kind": "radarr"}}})
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(out))
	}
	if out[0].Quality != Quality1080p || out[0].ConnectionID != "c1" ||
		out[0].Status != StatusDownloading || out[0].ExternalStatus != "grabbed" || out[0].Message != "in queue" {
		t.Fatalf("status[0] mismapped: %+v", out[0])
	}
	if out[1].Quality != Quality2160p || out[1].ConnectionID != "c2" || out[1].Status != StatusCompleted || out[1].ExternalStatus != "imported" {
		t.Fatalf("status[1] mismapped: %+v", out[1])
	}
	if got := fc.lastCheckReq.GetTargets(); len(got) != 2 ||
		got[0].GetConnectionId() != "c1" || got[0].GetExternalId() != "7" ||
		got[1].GetConnectionId() != "c2" || got[1].GetExternalId() != "8" {
		t.Fatalf("refs not forwarded: %+v", fc.lastCheckReq.GetTargets())
	}
}

func TestPluginRouterProviderListConfigOptionsTranslates(t *testing.T) {
	fc := &fakeRouterClient{optionsByField: map[string]*pluginv1.ConfigOptionList{
		"root_folder": {Options: []*pluginv1.ConfigOption{
			{Value: "/movies", Label: "Movies"},
			{Value: "/movies4k", Label: "Movies 4K"},
		}},
		"quality_profile_id": {Options: []*pluginv1.ConfigOption{
			{Value: "1", Label: "HD-1080p"},
		}},
	}}
	p := NewPluginRouterProvider(fakeRouterResolver{c: fc})
	out, err := p.ListConfigOptions(context.Background(), 1, "arr",
		ResolvedRouterConnection{ID: "c1", BaseURL: "http://r", APIKey: "k", Config: map[string]any{"service_kind": "radarr"}})
	if err != nil {
		t.Fatalf("ListConfigOptions: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 fields, got %d: %+v", len(out), out)
	}
	rf := out["root_folder"]
	if len(rf) != 2 || rf[0].Value != "/movies" || rf[0].Label != "Movies" || rf[1].Value != "/movies4k" || rf[1].Label != "Movies 4K" {
		t.Fatalf("root_folder mismapped: %+v", rf)
	}
	qp := out["quality_profile_id"]
	if len(qp) != 1 || qp[0].Value != "1" || qp[0].Label != "HD-1080p" {
		t.Fatalf("quality_profile_id mismapped: %+v", qp)
	}
}

func TestPluginRouterProviderValidateTranslates(t *testing.T) {
	fc := &fakeRouterClient{validateResp: &pluginv1.ValidateResponse{
		FieldErrors: map[string]string{"is_default": "cannot be 4K"}, FormError: "",
	}}
	p := NewPluginRouterProvider(fakeRouterResolver{c: fc})
	fe, form, err := p.Validate(context.Background(), 1, "arr",
		ResolvedRouterConnection{ID: "c1", Config: map[string]any{"service_kind": "radarr"}},
		nil)
	if err != nil || form != "" || fe["is_default"] != "cannot be 4K" {
		t.Fatalf("unexpected: fe=%v form=%q err=%v", fe, form, err)
	}
}
