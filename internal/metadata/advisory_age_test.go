package metadata

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// TestAdvisoryFromPluginMetadata covers the hostile-input contract of the
// advisory keys. A plugin owns this Struct completely and the value it carries
// is rendered on item detail, so anything that is not a whole age in range
// from a source Silo recognizes has to be dropped rather than stored.
func TestAdvisoryFromPluginMetadata(t *testing.T) {
	tests := []struct {
		name       string
		fields     map[string]any
		wantAge    int
		wantSource string
	}{
		{
			name:       "common sense age and source",
			fields:     map[string]any{"advisory_age": 13, "advisory_source": AdvisorySourceCommonSense},
			wantAge:    13,
			wantSource: AdvisorySourceCommonSense,
		},
		{
			name:       "mdblist is the other accepted source",
			fields:     map[string]any{"advisory_age": 17, "advisory_source": AdvisorySourceMDBList},
			wantAge:    17,
			wantSource: AdvisorySourceMDBList,
		},
		{
			name:       "source casing and padding are normalized",
			fields:     map[string]any{"advisory_age": 8, "advisory_source": "  CommonSense  "},
			wantAge:    8,
			wantSource: AdvisorySourceCommonSense,
		},
		{
			name:   "non-numeric age is dropped",
			fields: map[string]any{"advisory_age": "13", "advisory_source": AdvisorySourceCommonSense},
		},
		{
			name:   "fractional age is dropped rather than rounded",
			fields: map[string]any{"advisory_age": 12.5, "advisory_source": AdvisorySourceCommonSense},
		},
		{
			name:   "negative age is dropped",
			fields: map[string]any{"advisory_age": -3, "advisory_source": AdvisorySourceCommonSense},
		},
		{
			name:   "zero means unknown upstream and is dropped",
			fields: map[string]any{"advisory_age": 0, "advisory_source": AdvisorySourceCommonSense},
		},
		{
			name:   "out of range age is dropped",
			fields: map[string]any{"advisory_age": 99, "advisory_source": AdvisorySourceCommonSense},
		},
		{
			name:   "unknown source is dropped with its age",
			fields: map[string]any{"advisory_age": 13, "advisory_source": "totally-legit-ratings"},
		},
		{
			name:   "a source with no age yields nothing",
			fields: map[string]any{"advisory_source": AdvisorySourceCommonSense},
		},
		{
			name:   "an age with no source is not attributable, so it is dropped",
			fields: map[string]any{"advisory_age": 13},
		},
		{
			name:   "unrelated metadata keys are ignored",
			fields: map[string]any{"keywords": []any{"heist"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := structpb.NewStruct(tt.fields)
			if err != nil {
				t.Fatalf("structpb.NewStruct() error = %v", err)
			}
			age, source := advisoryFromPluginMetadata(value)
			if age != tt.wantAge || source != tt.wantSource {
				t.Fatalf("advisoryFromPluginMetadata() = (%d, %q), want (%d, %q)",
					age, source, tt.wantAge, tt.wantSource)
			}
		})
	}

	t.Run("nil struct", func(t *testing.T) {
		if age, source := advisoryFromPluginMetadata(nil); age != 0 || source != "" {
			t.Fatalf("advisoryFromPluginMetadata(nil) = (%d, %q), want (0, \"\")", age, source)
		}
	})
}

// TestPluginProviderGetMetadata_MapsAdvisoryAge proves the keys survive the
// whole provider call, not just the helper. The request has the shape the MDBList
// plugin really sees: the item carries the IMDb and TMDB IDs a primary provider
// resolved and no ID of MDBList's own, so only the declared lookup keys let the
// call through.
func TestPluginProviderGetMetadata_MapsAdvisoryAge(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{
		"advisory_age":    13,
		"advisory_source": AdvisorySourceCommonSense,
	})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}

	client := &fakePluginMetadataClient{
		response: &pluginv1.GetMetadataResponse{Item: &pluginv1.MetadataItem{
			ItemType:      "movie",
			ContentRating: "PG",
			Metadata:      metadata,
		}},
	}
	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		pluginInstallationIDSetting: "1",
		capabilityIDSetting:         "mdblist",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}
	provider.lookupProviderIDs = []string{"imdb", "tmdb"}

	result, err := provider.GetMetadata(context.Background(), MetadataRequest{
		ProviderIDs: map[string]string{"imdb": "tt0073195", "tmdb": "578"},
		ContentType: "movie",
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	switch {
	case client.getMetadataReq == nil:
		t.Fatal("expected the plugin to be called on its declared lookup IDs")
	case result == nil:
		t.Fatal("expected metadata result")
	case result.AdvisoryAge != 13 || result.AdvisorySource != AdvisorySourceCommonSense:
		t.Fatalf("advisory = (%d, %q), want (13, %q)",
			result.AdvisoryAge, result.AdvisorySource, AdvisorySourceCommonSense)
	// The advisory must never become the certification: they are different
	// numbers from different bodies, and only ContentRating feeds a ceiling.
	case result.ContentRating != "PG":
		t.Fatalf("ContentRating = %q, want \"PG\"", result.ContentRating)
	}
}

// TestMergeAdvisoryKeepsThePairTogether guards the invariant that makes the
// badge trustworthy: an age and the body that recommended it move as one, so
// no provider's number is ever shown under another's name.
func TestMergeAdvisoryKeepsThePairTogether(t *testing.T) {
	t.Run("fills an empty target", func(t *testing.T) {
		target := &MetadataResult{}
		source := &MetadataResult{AdvisoryAge: 13, AdvisorySource: AdvisorySourceCommonSense}
		MergeMetadata(source, target, nil, MergeFillEmpty)
		if target.AdvisoryAge != 13 || target.AdvisorySource != AdvisorySourceCommonSense {
			t.Fatalf("advisory = (%d, %q), want (13, \"commonsense\")", target.AdvisoryAge, target.AdvisorySource)
		}
	})

	t.Run("a source with no advisory never clears a stored one", func(t *testing.T) {
		target := &MetadataResult{AdvisoryAge: 13, AdvisorySource: AdvisorySourceCommonSense}
		MergeMetadata(&MetadataResult{}, target, nil, MergeReplaceUnlocked)
		if target.AdvisoryAge != 13 || target.AdvisorySource != AdvisorySourceCommonSense {
			t.Fatalf("advisory = (%d, %q), want it untouched", target.AdvisoryAge, target.AdvisorySource)
		}
	})

	t.Run("a half-populated source contributes nothing", func(t *testing.T) {
		target := &MetadataResult{AdvisoryAge: 13, AdvisorySource: AdvisorySourceCommonSense}
		MergeMetadata(&MetadataResult{AdvisoryAge: 18}, target, nil, MergeReplaceUnlocked)
		if target.AdvisoryAge != 13 || target.AdvisorySource != AdvisorySourceCommonSense {
			t.Fatalf("advisory = (%d, %q), want it untouched by a source with no attribution",
				target.AdvisoryAge, target.AdvisorySource)
		}
	})

	t.Run("replace swaps both halves together", func(t *testing.T) {
		target := &MetadataResult{AdvisoryAge: 13, AdvisorySource: AdvisorySourceCommonSense}
		MergeMetadata(&MetadataResult{AdvisoryAge: 18, AdvisorySource: AdvisorySourceMDBList}, target, nil, MergeReplaceUnlocked)
		if target.AdvisoryAge != 18 || target.AdvisorySource != AdvisorySourceMDBList {
			t.Fatalf("advisory = (%d, %q), want (18, \"mdblist\")", target.AdvisoryAge, target.AdvisorySource)
		}
	})

	t.Run("fill-empty does not overwrite an advisory already present", func(t *testing.T) {
		target := &MetadataResult{AdvisoryAge: 13, AdvisorySource: AdvisorySourceCommonSense}
		MergeMetadata(&MetadataResult{AdvisoryAge: 18, AdvisorySource: AdvisorySourceMDBList}, target, nil, MergeFillEmpty)
		if target.AdvisoryAge != 13 || target.AdvisorySource != AdvisorySourceCommonSense {
			t.Fatalf("advisory = (%d, %q), want the first provider's answer kept", target.AdvisoryAge, target.AdvisorySource)
		}
	})

	t.Run("the content-rating lock does not gate the advisory", func(t *testing.T) {
		target := &MetadataResult{ContentRating: "PG"}
		source := &MetadataResult{ContentRating: "R", AdvisoryAge: 13, AdvisorySource: AdvisorySourceCommonSense}
		MergeMetadata(source, target, []MetadataField{FieldContentRating}, MergeReplaceUnlocked)
		if target.ContentRating != "PG" {
			t.Fatalf("ContentRating = %q, want the locked \"PG\" kept", target.ContentRating)
		}
		if target.AdvisoryAge != 13 || target.AdvisorySource != AdvisorySourceCommonSense {
			t.Fatalf("advisory = (%d, %q), want (13, \"commonsense\"): it is not user-editable and has no lock",
				target.AdvisoryAge, target.AdvisorySource)
		}
	})
}

// TestPluginProviderGetMetadata_AdvisoryAgeOnlyForMoviesAndSeries pins that an
// advisory age reported for any other item type is dropped, so the profile
// advisory-age limit can never reach the beta book libraries.
func TestPluginProviderGetMetadata_AdvisoryAgeOnlyForMoviesAndSeries(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{
		"advisory_age":    13,
		"advisory_source": AdvisorySourceCommonSense,
	})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		pluginInstallationIDSetting: "1",
		capabilityIDSetting:         "books",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return &fakePluginMetadataClient{
			response: &pluginv1.GetMetadataResponse{Item: &pluginv1.MetadataItem{Title: "A Book", Metadata: metadata}},
		}, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}

	for contentType, want := range map[string]int{"movie": 13, "series": 13, "audiobook": 0, "ebook": 0, "podcast": 0, "": 0} {
		result, err := provider.GetMetadata(context.Background(), MetadataRequest{
			ProviderIDs: map[string]string{"books": "b-1"},
			ContentType: contentType,
		})
		if err != nil || result == nil {
			t.Fatalf("%q: GetMetadata() = %v, %v", contentType, result, err)
		}
		if result.AdvisoryAge != want || (want == 0) != (result.AdvisorySource == "") {
			t.Errorf("%q: advisory = (%d, %q), want age %d", contentType, result.AdvisoryAge, result.AdvisorySource, want)
		}
	}
}
