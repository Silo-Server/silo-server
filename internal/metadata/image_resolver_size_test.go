package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

type recordingTargetResolver struct {
	requests []artworkurl.TargetRequest
}

func (*recordingTargetResolver) ResolveArtworkURLs(context.Context, []string) map[string]artworkstore.ResolvedURL {
	return nil
}

func (*recordingTargetResolver) ResolveTargetURLs(context.Context, []artworkurl.Target, string) map[string]artworkstore.ResolvedURL {
	return nil
}

func (r *recordingTargetResolver) ResolveTargetRequests(_ context.Context, requests []artworkurl.TargetRequest) map[string]artworkstore.ResolvedURL {
	r.requests = append(r.requests, requests...)
	return nil
}

func TestProviderVariantForTargetUsesImageTypeLadder(t *testing.T) {
	tests := []struct {
		imageType string
		variant   string
		want      string
	}{
		{artworkkey.ImageTypePoster, artworkkey.VariantW300, imagesize.PluginVariantCard},
		{artworkkey.ImageTypePoster, artworkkey.VariantW500, imagesize.PluginVariantFeatured},
		{artworkkey.ImageTypePoster, artworkkey.VariantW780, imagesize.PluginVariantLarge},
		{artworkkey.ImageTypeLogo, artworkkey.VariantW1280, imagesize.PluginVariantLarge},
		// w1280 is not intrinsically "large": it is an intermediate backdrop
		// rung, which is why target slot context is part of the translation.
		{artworkkey.ImageTypeBackdrop, artworkkey.VariantW1280, imagesize.PluginVariantFeatured},
		{artworkkey.ImageTypeBackdrop, artworkkey.VariantW1920, imagesize.PluginVariantFeatured},
		{artworkkey.ImageTypeProfile, artworkkey.VariantW500, imagesize.PluginVariantFeatured},
		{artworkkey.ImageTypeProfile, artworkkey.OriginalVariant, imagesize.PluginVariantOriginal},
	}
	for _, tt := range tests {
		if got := providerVariantForTarget(tt.imageType, tt.variant); got != tt.want {
			t.Errorf("providerVariantForTarget(%q, %q) = %q, want %q", tt.imageType, tt.variant, got, tt.want)
		}
	}
}

func TestTargetRequestsKeepProviderReferencesAndVariants(t *testing.T) {
	resolver := NewPluginImageResolver()
	t.Cleanup(resolver.Close)
	recording := &recordingTargetResolver{}
	resolver.SetArtworkURLResolver(recording)

	requests := []artworkurl.TargetRequest{
		{Target: artworkurl.Target{Surface: artworkurl.SurfaceItemPosters, Keys: []string{"movie-1"}, Slot: artworkkey.ImageTypePoster}.WithReference("plug://poster.jpg"), Variant: artworkkey.VariantW780},
		{Target: artworkurl.Target{Surface: artworkurl.SurfaceItemLogos, Keys: []string{"movie-1"}, Slot: artworkkey.ImageTypeLogo}.WithReference("plug://logo.png"), Variant: artworkkey.VariantW1280},
	}
	resolver.ResolveArtworkTargetRequestsWithExpiry(t.Context(), requests)
	if len(recording.requests) != len(requests) {
		t.Fatalf("target resolver received %d requests, want %d", len(recording.requests), len(requests))
	}
	for i := range requests {
		if got, want := recording.requests[i], requests[i]; got.Target.Reference != want.Target.Reference || got.Variant != want.Variant {
			t.Errorf("request %d = reference %q variant %q, want reference %q variant %q", i, got.Target.Reference, got.Variant, want.Target.Reference, want.Variant)
		}
	}
}
