package notifications

import "testing"

func TestFlatItemKindRegistry(t *testing.T) {
	seenKinds := make(map[string]struct{})
	for _, k := range flatItemKinds {
		if _, dup := seenKinds[k.Kind]; dup {
			t.Fatalf("duplicate registry kind %q", k.Kind)
		}
		seenKinds[k.Kind] = struct{}{}
		if k.Kind == EventKindEpisode {
			t.Fatal("episodes are not a flat item kind")
		}
		if k.ItemType == "" || k.AvailabilityTable == "" {
			t.Fatalf("incomplete registry entry %+v", k)
		}
		if k.WantsToggle == nil || k.Selected == nil {
			t.Fatalf("registry entry %q missing accessors", k.Kind)
		}
	}
	for _, kind := range []string{EventKindMovie, EventKindAudiobook, EventKindEbook} {
		if _, ok := flatKindByString(kind); !ok {
			t.Errorf("flatKindByString(%q) not found", kind)
		}
	}
	for _, kind := range []string{EventKindEpisode, "", "music"} {
		if _, ok := flatKindByString(kind); ok {
			t.Errorf("flatKindByString(%q) unexpectedly found", kind)
		}
	}
}

func TestFlatItemKindToggleAndSelectionMapping(t *testing.T) {
	// Each kind's channel toggle and ingest-scope flag must read its own
	// field, not a neighbor's.
	toggles := map[string]ServerChannel{
		EventKindMovie:     {NotifyNewMovies: true},
		EventKindAudiobook: {NotifyNewAudiobooks: true},
		EventKindEbook:     {NotifyNewEbooks: true},
	}
	selections := map[string]AvailabilityKinds{
		EventKindMovie:     {Movies: true},
		EventKindAudiobook: {Audiobooks: true},
		EventKindEbook:     {Ebooks: true},
	}
	for _, k := range flatItemKinds {
		for kind, ch := range toggles {
			if got, want := k.WantsToggle(ch), kind == k.Kind; got != want {
				t.Errorf("kind %q WantsToggle(channel with only %q on) = %v, want %v",
					k.Kind, kind, got, want)
			}
		}
		for kind, sel := range selections {
			if got, want := k.Selected(sel), kind == k.Kind; got != want {
				t.Errorf("kind %q Selected(scope with only %q on) = %v, want %v",
					k.Kind, kind, got, want)
			}
		}
	}
}
