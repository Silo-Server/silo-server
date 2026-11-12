package requests

import (
	"context"
	"errors"
	"testing"
)

// optionsService is a server with one Seerr-like connection whose plugin
// lets the movie quality profile be chosen by whoever the connection's
// profile_chosen_by select names.
func optionsService(config map[string]any) (*Service, *fakeStore, *fakeRouterProvider) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	in := routerInst("seerr")
	in.PluginConfig = config
	store.integrations = []Integration{in}
	service, router := newRoutedService(store)
	router.requesterOptions = map[int][]RouterRequesterOption{1: {
		{Field: "radarr_profile_id", MediaType: MediaTypeMovie, Label: "Quality", ChooserField: "profile_chosen_by"},
		{Field: "sonarr_profile_id", MediaType: MediaTypeSeries, Label: "Quality", ChooserField: "profile_chosen_by"},
		// A plugin must not be able to hand requesters a key routing owns.
		{Field: configIs4K},
	}}
	router.options = map[string][]RouterOption{
		"radarr_profile_id": {{Value: "default", Label: "Seerr default (HD-1080p)"}, {Value: "4", Label: "Ultra-HD"}},
		"sonarr_profile_id": {{Value: "default", Label: "Seerr default"}, {Value: "9", Label: "Series UHD"}},
		configIs4K:          {{Value: "true", Label: "Yes"}},
	}
	return service, store, router
}

func TestRequesterOptionsOffersOnlyWhatTheServerAllows(t *testing.T) {
	service, _, _ := optionsService(map[string]any{"profile_chosen_by": "requester", "radarr_profile_id": "default"})
	options, err := service.RequesterOptions(context.Background(), testViewer(1), MediaTypeMovie, 550)
	if err != nil {
		t.Fatalf("RequesterOptions: %v", err)
	}
	if len(options) != 1 || options[0].Key != "radarr_profile_id" || options[0].Label != "Quality" ||
		options[0].Default != "default" || len(options[0].Choices) != 2 {
		t.Fatalf("options = %+v, want the movie quality profile only", options)
	}

	// The admin has not turned the choice on: nothing is offered, and the
	// plugin is not asked.
	service, _, router := optionsService(map[string]any{"profile_chosen_by": "admin"})
	router.optionsErr = errors.New("must not be called")
	options, err = service.RequesterOptions(context.Background(), testViewer(1), MediaTypeMovie, 550)
	if err != nil || len(options) != 0 {
		t.Fatalf("options = %+v, err = %v; want none", options, err)
	}

	// A download server that cannot list its choices offers none rather
	// than failing the request dialog.
	service, _, router = optionsService(map[string]any{"profile_chosen_by": "requester"})
	router.optionsErr = errors.New("seerr is down")
	options, err = service.RequesterOptions(context.Background(), testViewer(1), MediaTypeMovie, 550)
	if err != nil || len(options) != 0 {
		t.Fatalf("options = %+v, err = %v; want none", options, err)
	}
}

func TestCreateRequestSendsTheRequestersPick(t *testing.T) {
	service, store, router := optionsService(map[string]any{"profile_chosen_by": "requester", "radarr_profile_id": "default"})
	input := fightClubInput
	input.Options = map[string]string{"radarr_profile_id": "4"}
	req, err := service.CreateRequest(context.Background(), testViewer(1), input)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	want := RequesterChoice{Key: "radarr_profile_id", Label: "Quality", Value: "4", ValueLabel: "Ultra-HD"}
	stored := store.requests[req.ID].RequesterChoices
	if stored.IntegrationID != "seerr" || len(stored.Choices) != 1 || stored.Choices[0] != want {
		t.Fatalf("stored choices = %+v, want %+v on seerr", stored, want)
	}
	// Auto-approval submitted it: the plugin got the pick as its config.
	if len(router.gotConns) != 1 || router.gotConns[0].Config["radarr_profile_id"] != "4" {
		t.Fatalf("fulfill connections = %+v, want radarr_profile_id 4", router.gotConns)
	}
	// The saved server config is untouched.
	if got := store.integrations[0].PluginConfig["radarr_profile_id"]; got != "default" {
		t.Fatalf("server config radarr_profile_id = %v, want default", got)
	}
}

func TestCreateRequestRefusesPicksThatAreNotOffered(t *testing.T) {
	for name, c := range map[string]struct {
		config     map[string]any
		picks      map[string]string
		optionsErr error
	}{
		"value not listed":       {map[string]any{"profile_chosen_by": "requester"}, map[string]string{"radarr_profile_id": "7"}, nil},
		"other media type":       {map[string]any{"profile_chosen_by": "requester"}, map[string]string{"sonarr_profile_id": "9"}, nil},
		"undeclared key":         {map[string]any{"profile_chosen_by": "requester"}, map[string]string{"radarr_root_folder": "/etc"}, nil},
		"routing-owned key":      {map[string]any{"profile_chosen_by": "requester"}, map[string]string{configIs4K: "true"}, nil},
		"choice turned off":      {map[string]any{"profile_chosen_by": "admin"}, map[string]string{"radarr_profile_id": "4"}, nil},
		"choices cannot be read": {map[string]any{"profile_chosen_by": "requester"}, map[string]string{"radarr_profile_id": "4"}, errors.New("seerr is down")},
	} {
		t.Run(name, func(t *testing.T) {
			service, store, router := optionsService(c.config)
			router.optionsErr = c.optionsErr
			input := fightClubInput
			input.Options = c.picks
			_, err := service.CreateRequest(context.Background(), testViewer(1), input)
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.FieldErrors["options"] == "" {
				t.Fatalf("CreateRequest error = %v, want a validation error on options", err)
			}
			if len(store.created) != 0 || router.fulfillCalls != 0 {
				t.Fatalf("created = %d, fulfill calls = %d; want nothing made or sent", len(store.created), router.fulfillCalls)
			}
		})
	}
}

// A pick is an id or path of the server it was offered on. It must not reach
// another server, or one whose admin has since turned the choice off.
func TestSubmitLeavesOutPicksThatNoLongerApply(t *testing.T) {
	pick := RequesterChoices{IntegrationID: "seerr", Choices: []RequesterChoice{{Key: "radarr_profile_id", Value: "4"}}}
	for name, c := range map[string]struct {
		config  map[string]any
		choices RequesterChoices
	}{
		"choice turned off": {map[string]any{"profile_chosen_by": "admin", "radarr_profile_id": "default"}, pick},
		"another server": {map[string]any{"profile_chosen_by": "requester", "radarr_profile_id": "default"},
			RequesterChoices{IntegrationID: "old-seerr", Choices: pick.Choices}},
		"option no longer declared": {map[string]any{"profile_chosen_by": "requester", "radarr_profile_id": "default"},
			RequesterChoices{IntegrationID: "seerr", Choices: []RequesterChoice{{Key: "requester_mode", Value: "admin"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			service, store, router := optionsService(c.config)
			req := Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive,
				RequestedByUserID: 7, RequesterChoices: c.choices}
			store.requests["r1"] = &req
			if _, err := service.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
				t.Fatalf("submit: %v", err)
			}
			if len(router.gotConns) != 1 {
				t.Fatalf("fulfill connections = %+v, want one", router.gotConns)
			}
			config := router.gotConns[0].Config
			if config["radarr_profile_id"] != "default" {
				t.Fatalf("radarr_profile_id = %v, want the server's own", config["radarr_profile_id"])
			}
			if _, set := config["requester_mode"]; set {
				t.Fatalf("config = %v, an undeclared key was set", config)
			}
		})
	}
}

func TestApproveWithOptionsAppliesTheAdminsPick(t *testing.T) {
	// Requesters are not offered the choice; the approving admin still is.
	service, store, router := optionsService(map[string]any{"profile_chosen_by": "approver", "radarr_profile_id": "default"})
	store.settings.GlobalAutoApprovalEnabled = false
	if options, err := service.RequesterOptions(context.Background(), testViewer(1), MediaTypeMovie, 550); err != nil || len(options) != 0 {
		t.Fatalf("RequesterOptions = %+v, err = %v; want none for a requester", options, err)
	}
	req, err := service.CreateRequest(context.Background(), testViewer(1), fightClubInput)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	admin := Viewer{UserID: 9, ProfileID: "9", IsAdmin: true}

	if _, err := service.ApproverOptions(context.Background(), testViewer(1), req.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ApproverOptions as a requester: err = %v, want ErrForbidden", err)
	}
	options, err := service.ApproverOptions(context.Background(), admin, req.ID)
	if err != nil || len(options) != 1 || options[0].Key != "radarr_profile_id" || options[0].Default != "default" {
		t.Fatalf("ApproverOptions = %+v, err = %v; want the movie quality profile", options, err)
	}

	var invalid *ValidationError
	if _, err := service.ApproveWithOptions(context.Background(), admin, req.ID, map[string]string{"radarr_profile_id": "77"}); !errors.As(err, &invalid) {
		t.Fatalf("ApproveWithOptions with an unlisted value: err = %v, want a validation error", err)
	}
	if len(router.gotConns) != 0 {
		t.Fatalf("a refused approval submitted the request: %+v", router.gotConns)
	}

	if _, err := service.ApproveWithOptions(context.Background(), admin, req.ID, map[string]string{"radarr_profile_id": "4"}); err != nil {
		t.Fatalf("ApproveWithOptions: %v", err)
	}
	want := RequesterChoice{Key: "radarr_profile_id", Label: "Quality", Value: "4", ValueLabel: "Ultra-HD", ByAdmin: true}
	stored := store.requests[req.ID].RequesterChoices
	if stored.IntegrationID != "seerr" || len(stored.Choices) != 1 || stored.Choices[0] != want {
		t.Fatalf("stored choices = %+v, want %+v on seerr", stored, want)
	}
	if len(router.gotConns) != 1 || router.gotConns[0].Config["radarr_profile_id"] != "4" {
		t.Fatalf("fulfill connections = %+v, want radarr_profile_id 4", router.gotConns)
	}
}

func TestApproverOptionsDefaultToTheRequestersPick(t *testing.T) {
	service, store, _ := optionsService(map[string]any{"profile_chosen_by": "requester", "radarr_profile_id": "default"})
	store.settings.GlobalAutoApprovalEnabled = false
	input := fightClubInput
	input.Options = map[string]string{"radarr_profile_id": "4"}
	req, err := service.CreateRequest(context.Background(), testViewer(1), input)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	options, err := service.ApproverOptions(context.Background(), Viewer{UserID: 9, ProfileID: "9", IsAdmin: true}, req.ID)
	if err != nil || len(options) != 1 || options[0].Default != "4" {
		t.Fatalf("ApproverOptions = %+v, err = %v; want the requester's pick as the default", options, err)
	}
}

func TestApproverOptionsLeaveOutWhatTheAdminFixed(t *testing.T) {
	service, store, router := optionsService(map[string]any{"profile_chosen_by": "admin", "radarr_profile_id": "default"})
	store.settings.GlobalAutoApprovalEnabled = false
	req, err := service.CreateRequest(context.Background(), testViewer(1), fightClubInput)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	admin := Viewer{UserID: 9, ProfileID: "9", IsAdmin: true}
	if options, err := service.ApproverOptions(context.Background(), admin, req.ID); err != nil || len(options) != 0 {
		t.Fatalf("ApproverOptions = %+v, err = %v; want none", options, err)
	}
	var invalid *ValidationError
	if _, err := service.ApproveWithOptions(context.Background(), admin, req.ID, map[string]string{"radarr_profile_id": "4"}); !errors.As(err, &invalid) {
		t.Fatalf("ApproveWithOptions for a fixed option: err = %v, want a validation error", err)
	}
	if len(router.gotConns) != 0 {
		t.Fatalf("a refused approval submitted the request: %+v", router.gotConns)
	}
}
