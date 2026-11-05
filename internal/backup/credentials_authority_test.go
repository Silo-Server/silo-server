package backup

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestAuthorityComparisonCoversEveryField: every field of the policies an
// account's or a grant's authority is compared by widens it when loosened,
// so a new field cannot reach a restore without a decision. A field
// deliberately left out is listed with why.
func TestAuthorityComparisonCoversEveryField(t *testing.T) {
	ten, two := 10, 2
	strictAccess := access.EffectiveUserPolicy{LibraryIDs: []int{1}, MaxPlaybackQuality: "720p", MaxStreams: 1, MaxTranscodes: 1,
		MaxRemoteStreamBitrateKbps: 1000, MaxLocalStreamBitrateKbps: 1000, MaxProfiles: 1, Permissions: []string{}}
	accessWidenings := map[string]func(*access.EffectiveUserPolicy){
		"LibraryIDs":                 func(p *access.EffectiveUserPolicy) { p.LibraryIDs = nil },
		"MaxPlaybackQuality":         func(p *access.EffectiveUserPolicy) { p.MaxPlaybackQuality = "" },
		"DownloadAllowed":            func(p *access.EffectiveUserPolicy) { p.DownloadAllowed = true },
		"DownloadTranscodeAllowed":   func(p *access.EffectiveUserPolicy) { p.DownloadTranscodeAllowed = true },
		"TranscodeAllowed":           func(p *access.EffectiveUserPolicy) { p.TranscodeAllowed = true },
		"AudioTranscodeAllowed":      func(p *access.EffectiveUserPolicy) { p.AudioTranscodeAllowed = true },
		"MaxStreams":                 func(p *access.EffectiveUserPolicy) { p.MaxStreams = 0 },
		"MaxTranscodes":              func(p *access.EffectiveUserPolicy) { p.MaxTranscodes = 0 },
		"MaxRemoteStreamBitrateKbps": func(p *access.EffectiveUserPolicy) { p.MaxRemoteStreamBitrateKbps = 0 },
		"MaxLocalStreamBitrateKbps":  func(p *access.EffectiveUserPolicy) { p.MaxLocalStreamBitrateKbps = 0 },
		"MaxProfiles":                func(p *access.EffectiveUserPolicy) { p.MaxProfiles = 5 },
		"Permissions":                func(p *access.EffectiveUserPolicy) { p.Permissions = []string{"marker_edit"} },
		"RequestsAllowed":            func(p *access.EffectiveUserPolicy) { p.RequestsAllowed = true },
	}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[access.EffectiveUserPolicy]()) {
		widen, ok := accessWidenings[f.Name]
		if !ok {
			t.Errorf("access policy field %s has no widening case: add one, and compare it in access.WiderThan", f.Name)
			continue
		}
		loose := strictAccess
		widen(&loose)
		if len(access.WiderThan(loose, strictAccess)) == 0 {
			t.Errorf("access.WiderThan misses a looser %s", f.Name)
		}
	}

	strictRequests := requests.EffectivePolicy{RequestsEnabled: true, MaxRequests: 2, WindowDays: 10}
	requestWidenings := map[string]func(*requests.EffectivePolicy){
		"RequestsEnabled": nil, // compared against a live policy that has it off, below
		"MaxRequests":     func(p *requests.EffectivePolicy) { p.MaxRequests = ten },
		"WindowDays":      func(p *requests.EffectivePolicy) { p.WindowDays = two },
		"Unlimited":       func(p *requests.EffectivePolicy) { p.Unlimited = true },
		"Blocked":         nil, // as RequestsEnabled
		"AutoApprove":     func(p *requests.EffectivePolicy) { p.AutoApprove = true },
	}
	// Usage, not authority.
	usage := map[string]bool{"Used": true, "Remaining": true, "WindowStart": true}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[requests.EffectivePolicy]()) {
		widen, ok := requestWidenings[f.Name]
		switch {
		case usage[f.Name]:
		case !ok:
			t.Errorf("request policy field %s has no widening case: add one, and compare it in requestsWider", f.Name)
		case widen != nil:
			loose := strictRequests
			widen(&loose)
			if !requestsWider(loose, strictRequests) {
				t.Errorf("requestsWider misses a looser %s", f.Name)
			}
		}
	}
	for name, live := range map[string]requests.EffectivePolicy{
		"RequestsEnabled": {MaxRequests: 2, WindowDays: 10},
		"Blocked":         {RequestsEnabled: true, Blocked: true, MaxRequests: 2, WindowDays: 10},
	} {
		if !requestsWider(strictRequests, live) {
			t.Errorf("requestsWider misses requests allowed where live has %s", name)
		}
	}

	// The account's own fields, each compared by widerThan.
	accountFields := map[string]bool{"rank": true, "enabled": true, "breakGlass": true, "policy": true, "requests": true, "libraries": true, "profiles": true}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[accountAuthority]()) {
		if !accountFields[f.Name] {
			t.Errorf("accountAuthority field %s is not compared: add it to widerThan and here", f.Name)
		}
	}
}

// TestProfileComparisonCoversEveryField: every field of a stored profile is
// a protection or restriction profileLimits compares, or is named as no
// authority with why; each, lifted, widens the account.
func TestProfileComparisonCoversEveryField(t *testing.T) {
	profileColumns := map[string]string{
		"PINHash": "pin", "IsPrimary": "primary", "IsChild": "child", "MaxContentRating": "ceiling", "MaxAdvisoryAge": "advisoryAge",
		"RequireAdvisoryAge": "requireAdvisoryAge", "LibraryRestrictionsEnabled": "restricted", "AllowedLibraryIDs": "libraries",
		"MaxPlaybackQuality": "quality", "AllowedTitleIDs": "allowedTitles",
	}
	notAuthority := map[string]string{
		"ID": "identity", "Name": "display", "Avatar": "display", "CreatedAt": "bookkeeping", "UpdatedAt": "bookkeeping",
		// Verification tokens are bound to it, and a full restore replaces
		// the signing keys, so no earlier token survives either way.
		"PINRevision":       "token binding",
		"QualityPreference": "preference", "Language": "preference", "PreferredMetadataLanguage": "preference",
		"SubtitleLanguage": "preference", "SubtitleMode": "preference", "AutoSkipIntro": "preference", "AutoSkipCredits": "preference",
		"AutoSkipRecap": "preference", "AutoPlayNextPreview": "preference", "ShowForcedSubtitles": "preference",
	}
	compared := map[string]bool{}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[userstore.Profile]()) {
		field, ok := profileColumns[f.Name]
		if _, why := notAuthority[f.Name]; !ok && !why {
			t.Errorf("profile field %s is neither compared nor listed as no authority: add it to profileLimits and looserThan, or say why not", f.Name)
		}
		compared[field] = ok
	}
	// Raw allowance rows, uncapped: no Profile field carries them.
	compared["allowedCollections"] = true
	compared["directTitles"] = true
	for _, f := range reflect.VisibleFields(reflect.TypeFor[profileLimits]()) {
		if !compared[f.Name] {
			t.Errorf("profileLimits field %s maps to no profile field", f.Name)
		}
	}
	// Each protection, lifted, widens the account.
	quality, age := "1080p", 12
	strict := profileLimits{pin: true, child: true, ceiling: "PG", advisoryAge: &age, requireAdvisoryAge: true, restricted: true,
		libraries: []int{1}, quality: quality}
	profileWidenings := map[string]func(*profileLimits){
		"pin":                func(p *profileLimits) { p.pin = false },
		"primary":            func(p *profileLimits) { p.primary = true },
		"child":              func(p *profileLimits) { p.child = false },
		"ceiling":            func(p *profileLimits) { p.ceiling = "" },
		"advisoryAge":        func(p *profileLimits) { p.advisoryAge = nil },
		"requireAdvisoryAge": func(p *profileLimits) { p.requireAdvisoryAge = false },
		"restricted":         func(p *profileLimits) { p.restricted = false },
		"libraries":          func(p *profileLimits) { p.libraries = []int{1, 2} },
		"quality":            func(p *profileLimits) { p.quality = "" },
		"allowedTitles":      func(p *profileLimits) { p.allowedTitles = []string{"movie-r"} },
		"allowedCollections": func(p *profileLimits) { p.allowedCollections = []string{"family-night"} },
		"directTitles":       func(p *profileLimits) { p.directTitles = []string{"movie-r"} },
	}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[profileLimits]()) {
		widen, ok := profileWidenings[f.Name]
		if !ok {
			t.Errorf("profileLimits field %s has no widening case", f.Name)
			continue
		}
		loose := strict
		widen(&loose)
		if !loose.looserThan(strict) {
			t.Errorf("looserThan misses a looser %s", f.Name)
		}
	}
	// A household manager without a PIN, where the live one has one, under
	// another profile id too.
	live := accountAuthority{profiles: map[string]profileLimits{"a": {primary: true, pin: true}}}
	restored := accountAuthority{profiles: map[string]profileLimits{"b": {primary: true}}}
	if !restored.widerThan(live) {
		t.Error("widerThan misses a primary profile that lost its PIN")
	}
	// A profile deleted here since the backup, however restricted.
	revived := accountAuthority{profiles: map[string]profileLimits{"a": {primary: true, pin: true}, "b": strict}}
	if !revived.widerThan(live) {
		t.Error("widerThan misses a profile only the restored data has")
	}
}
