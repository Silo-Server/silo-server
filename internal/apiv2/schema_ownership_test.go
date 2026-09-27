package apiv2

import (
	"strings"
	"testing"
)

// Approved domain-owned wire values: playback and offline protocols, catalog
// projections, template definitions, onboarding flows, and provider reports.
// Transport changes to these values still require reviewing the generated
// contract diff. New external types need an explicit ownership decision here.
var approvedDomainSchemaTypes = map[string]string{
	"github.com/Silo-Server/silo-server/internal/auth":                  "APIKeyScope",
	"github.com/Silo-Server/silo-server/internal/catalog":               "AudiobookDetailExtension AudiobookNarration AudiobookPerson AudiobookRelatedContent AudiobookRelatedItem AudiobookSeriesGroup CastCredit CrewCredit EbookDetailExtension ItemExtraInfo ItemVideoInfo MangaChapter MangaDetailExtension Marker SubtitleInfo VersionChapter VersionSubtitleTrack",
	"github.com/Silo-Server/silo-server/internal/catalogseed":           "ImportResult PathRewrite",
	"github.com/Silo-Server/silo-server/internal/collections/templates": "Bundle BundleCatalog Catalog CategoryGroup MDBListSpec TMDBCollectionSpec TMDBDiscoverSpec TMDBSpec Template TraktSpec",
	"github.com/Silo-Server/silo-server/internal/diagnostics":           "IngestResult",
	"github.com/Silo-Server/silo-server/internal/downloads":             "OfflineAudioTrack OfflineChapter OfflineIdentity OfflineIntegrity OfflineSubtitle SkippedDownload SkippedManifest",
	"github.com/Silo-Server/silo-server/internal/historyimport":         "ExternalUser",
	"github.com/Silo-Server/silo-server/internal/jellycompat":           "ConnectInfo WebInstallerPrerequisite",
	"github.com/Silo-Server/silo-server/internal/metadata":              "MatchCandidate TitleAlias",
	"github.com/Silo-Server/silo-server/internal/models":                "AudioTrack VideoTrack",
	"github.com/Silo-Server/silo-server/internal/onboarding":            "Flow SettingOption SettingSpec Step StepLink",
	"github.com/Silo-Server/silo-server/internal/playback":              "AppliedQuirkV3 AudioClaimsV3 AudioPassthroughEntryV3 AudioPassthroughV3 AvailableQualityV3 ClientCapabilities ClientCodecCapabilitiesV3 ClientPlaybackContextV3 DegradationWarningV3 DeliveryCapabilityV3 DeliverySubtitleCapabilitiesV3 DetectedBackend DeviceContextV3 DolbyVisionProfileCapabilityV3 EffectiveRecipeV3 EmbeddedSubtitleV3 FailureV3 HDRCapabilitiesV3 NativeEmbeddedSubtitleCapabilityV3 OutputContextV3 OutputDisplayV3 RenderDeviceInfo SelectedTracksV3 StreamV3 SubtitleArtifactV3 SubtitleClaimsV3 SubtitleDecisionV3 SubtitleInventoryItemV3 TerminalV3 TimelineV3 TrackIdentityV3 TransformationV3 ValidationClaimsV3 VideoClaimsV3 VideoDecodeCapabilityV3",
	"github.com/Silo-Server/silo-server/internal/policy":                "CompileIssue",
	"github.com/Silo-Server/silo-server/internal/requests":              "FeatureStatus RouterOption",
	"github.com/Silo-Server/silo-server/internal/streamtelemetry":       "ParityMismatch ParityReport",
	"github.com/Silo-Server/silo-server/internal/subtitles/ai":          "QuotaStatus",
	"github.com/Silo-Server/silo-server/internal/watchsync":             "ConnectionUpdate",
	"github.com/Silo-Server/silo-server/internal/webhooksync":           "DiscoveredUser RotateWebhookResult UpdateConnectionInput",
	"github.com/danielgtaylor/huma/v2":                                  "FormFile",
}

func TestNativeSchemaOwnership(t *testing.T) {
	allowed := map[string]bool{}
	for pkg, names := range approvedDomainSchemaTypes {
		for _, name := range strings.Fields(names) {
			allowed[pkg+"."+name] = true
		}
	}
	NewHandler(Dependencies{testRegister: func(reg *Registry) {
		schemas := reg.api.OpenAPI().Components.Schemas
		for name := range schemas.Map() {
			typ := schemas.TypeFromRef("#/components/schemas/" + name)
			if typ == nil || typ.PkgPath() == "" || strings.HasSuffix(typ.PkgPath(), "/apiv2") {
				continue
			}
			if !allowed[typ.PkgPath()+"."+typ.Name()] {
				t.Errorf("schema %s is owned by unreviewed transport type %s.%s; project it into apiv2 or document shared domain ownership", name, typ.PkgPath(), typ.Name())
			}
		}
		for _, forbidden := range []string{"Bundle", "Catalog", "Step", "Flow", "Template", "Capabilities", "IngestResult", "PathRewrite", "AdminPluginFormOption", "AdminFormOptionView", "AdminFormView", "ConfigSchemaView"} {
			if _, exists := schemas.Map()[forbidden]; exists {
				t.Errorf("ambiguous or duplicate schema %s is published", forbidden)
			}
		}
	}})
}
