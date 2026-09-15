package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/textproto"
	"runtime/debug"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	mediacatalog "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Prefix is the path every v2 operation lives under. Operations register with
// their full path because the API listener hands the subtree over with a
// wildcard Handle, not a Mount, so the inner router sees the whole URL.
const Prefix = "/api/v2"

// DelegationPattern is the API listener's registration that hands the v2
// subtree to this package.
const DelegationPattern = Prefix + "/*"

// APIMajor is the contract major this package serves.
const APIMajor = 2

// MaxJSONBodyBytes is the default structured request-body cap, enforced
// before the body is decoded. An operation may lower it; raising it needs an
// explicit limit, rationale and boundary tests in that operation's contract
// ledger (docs/architecture/api-contract.md, "HTTP representation").
const MaxJSONBodyBytes int64 = 1 << 20

// BodyReadTimeout is the structured-body read deadline. It mirrors the
// server's 30 s ReadTimeout baseline rather than Huma's 5 s default.
// Ratified on #135, 2026-09-02.
const BodyReadTimeout = 30 * time.Second

func init() {
	// Response slices and maps are never null on the wire; builders
	// initialize them (see Collection) and the schema says so.
	huma.DefaultArrayNullable = false
	installErrorAdapter()
}

// Dependencies is the runtime wiring the v2 listener composes onto
// operations. Every field is optional: a missing gate never removes a route,
// it makes the operations behind that gate fail closed with a typed problem.
type Dependencies struct {
	// ObserveRoutes receives detached method/path values after this actual router
	// is fully registered. It cannot mutate or recover the sealed router.
	ObserveRoutes func([]streamtelemetry.WalkedRoute)

	DirectDownloads                 *DirectDownloadHandlers
	ViewerSubtitleDelete            ViewerSubtitleDeleteService
	OrderedApplePush                OrderedApplePushService
	NotificationEmailVerification   NotificationEmailVerificationService
	AdminAutoscanSourceDeletes      AdminAutoscanSourceDeleteService
	AdminAutoscanSourceWrites       AdminAutoscanSourceWriteService
	WatchTogetherSocket             WatchTogetherSocketService
	WatchTogetherCreate             WatchTogetherCreateService
	AdminAutoscanEvents             AdminAutoscanEventsService
	AdminAutoscanScans              AdminAutoscanScansService
	WatchTogetherSuggestionPromote  WatchTogetherSuggestionPromoteService
	WatchTogetherSuggestionCreate   WatchTogetherSuggestionCreateService
	AdminAutoscanConnectionDeletes  AdminAutoscanConnectionDeleteService
	AdminAutoscanConnectionUpdate   AdminAutoscanConnectionUpdateService
	AdminAutoscanConnectionCreation AdminAutoscanConnectionCreationService
	AdminAutoscanRewrites           AdminAutoscanRewritesService
	AdminAutoscanAvailableSources   AdminAutoscanAvailableSourcesService
	AdminAuditLogs                  AdminAuditLogsService
	AdminOperationalLogs            AdminOperationalLogsService
	AdminJellyfinCompatSettings     AdminJellyfinCompatSettingsService
	OrderedAndroidPush              OrderedAndroidPushService
	WatchTogetherSuggestionDelete   WatchTogetherSuggestionDeleteService
	WatchTogetherClose              WatchTogetherCloseService
	WatchTogetherRoomRead           WatchTogetherRoomReadService
	WatchTogetherPolicy             WatchTogetherPolicyService
	WatchTogetherJoin               WatchTogetherJoinService
	WatchTogetherSelection          WatchTogetherSelectionService
	WatchTogetherSuggestions        WatchTogetherSuggestionService
	AdminSectionSettingsWrite       AdminSectionSettingsWriteService
	AdminDashboardStats             AdminDashboardStatsService
	AdminHardwareAcceleration       AdminHardwareAccelerationService
	AdminDashboardLayout            AdminDashboardLayoutService
	AdminDashboardLayoutResets      AdminDashboardLayoutResetService
	AdminDashboardLayoutSaves       AdminDashboardLayoutSaveService
	ScanControls                    ScanControlService
	AuthProviderIconPublic          AuthProviderIconPublic
	AdminPlaybackSessions           AdminPlaybackSessionService
	AdminNodeSessions               AdminNodeSessionService
	AdminPlaybackCommands           AdminPlaybackCommandService
	AdminPlaybackTerminate          AdminPlaybackTerminateService
	AdminAutoscanConnections        AdminAutoscanConnectionsService
	AdminAutoscanInspection         AdminAutoscanInspectionService
	AdminAutoscanSources            AdminAutoscanSourcesService
	AutoscanDelivery                AutoscanDeliveryService
	Onboarding                      OnboardingService
	DownloadCreation                DownloadCreationService
	AdminJellyfinCompatStatus       AdminJellyfinCompatStatusService
	AdminSettingRead                AdminSettingReadService
	AdminJellyfinCompatWeb          AdminJellyfinCompatWebService
	AdminRateLimitsWrite            AdminRateLimitWriteService
	AdminRateLimits                 AdminRateLimitReadService
	DownloadSubscriptionMutations   DownloadSubscriptionMutationService
	DownloadSubscriptionSync        DownloadSubscriptionSyncService
	DiagnosticsChunks               DiagnosticsChunksService
	DiagnosticsIngress              DiagnosticsIngressService
	EventsSocket                    EventsSocketService
	AdminLogsSocket                 AdminLogsSocketService
	PlaybackControlSocket           PlaybackControlSocketService
	EventsCapability                EventsCapabilityService
	NetworkAccess                   NetworkAccessService
	NotificationDestinationCreate   NotificationDestinationCreateService
	AdminUnmatchedFiles             AdminUnmatchedFilesService
	AdminCatalogImages              AdminCatalogImagesService
	AdminCatalogMatch               AdminCatalogMatchService
	AdminCatalogSplit               AdminCatalogSplitService
	AdminMarkerContributions        AdminMarkerContributionsService
	AdminMarkerProviders            AdminMarkerProvidersService
	AdminMarkerHistory              AdminMarkerHistoryService
	AdminEpisodeMarkers             AdminEpisodeMarkersService
	DownloadSubscriptions           DownloadSubscriptionService
	DownloadManifests               DownloadManifestService
	DownloadDelivery                DownloadDeliveryService
	Downloads                       DownloadRegistryService
	DownloadProxyDelivery           func() bool
	WebhookReceiver                 WebhookReceiverService
	SubtitleAIReads                 SubtitleAIReadService
	NotificationDiscordLinks        NotificationDiscordLinkService
	NotificationEmailLinks          NotificationEmailLinkService
	NotificationDestinationTests    NotificationDestinationTestService
	AdminDevices                    AdminDeviceService
	UserLibraries                   UserLibraryService
	EbookAnnotations                EbookAnnotationService
	AdminNotificationDiscord        AdminNotificationDiscordService
	NotificationDestinations        NotificationDestinationService
	EbookFiles                      EbookFileService
	EbookConfig                     EbookConfigService
	EbookProgress                   EbookProgressService
	AdminItemMetadata               AdminItemMetadataService
	AdminMetadataTranslation        AdminMetadataTranslationService
	AdminPeople                     AdminPeopleService
	AdminDiagnosticDownloads        AdminDiagnosticDownloadService
	AdminDiagnosticReads            AdminDiagnosticReadsService
	AdminDashboardInsights          AdminDashboardInsightsService
	AdminNodesRead                  AdminNodesReadService
	AdminNodeCommands               AdminNodeCommandsService
	AdminNodeReload                 AdminNodeReloadService
	AdminNodeConfiguration          AdminNodeConfigurationService
	AdminServerStatus               AdminServerStatusService
	AdminServerRestart              AdminServerRestartService
	AdminBrandingAssets             AdminBrandingAssetService
	AdminEmailTests                 AdminEmailTestService
	AdminSourceWebhookLifecycle     AdminAutoscanWebhookLifecycleService
	AdminAutoscanSettingsUpdates    AdminAutoscanSettingsUpdateService
	AdminDiagnosticDeletes          AdminDiagnosticDeleteService
	AdminAutoscanConnectionTests    AdminAutoscanConnectionTestService
	AdminPluginRepositoryCreation   AdminPluginRepositoryCreationService
	AdminPluginRepositoryUpdates    AdminPluginRepositoryUpdateService
	AdminPluginRepositoryDeletes    AdminPluginRepositoryDeleteService
	AdminPluginInventory            AdminPluginInventoryService
	AdminPluginConfiguration        AdminPluginConfigurationService
	AdminPluginLifecycle            AdminPluginLifecycleService
	AdminPluginUploads              AdminPluginUploadService
	AdminTelemetryParity            AdminTelemetryParityService
	AdminPluginRepositories         AdminPluginRepositoriesService
	AdminPluginCatalogSettings      AdminPluginCatalogSettingsService
	AdminRecommendations            AdminRecommendationsService
	AdminLiteraryWorks              AdminLiteraryWorkService
	NotificationChannels            NotificationChannelService
	NotificationRelay               NotificationRelayService
	AdminNotificationPush           AdminNotificationPushService
	NotificationInbox               NotificationInboxService
	CompatConnectInfo               CompatConnectInfoService
	AdminCatalogTransfer            AdminCatalogTransferService
	AdminCatalogSearch              AdminCatalogSearchService
	AdminSettingsChecks             AdminSettingsCheckService
	AdminSettingsInspection         AdminSettingsInspectionService
	AdminResourceSampler            AdminResourceSampler
	AdminTaskJobs                   AdminTaskJobsService
	AdminCatalogSources             AdminCatalogSourcesService
	AdminFilesystem                 AdminFilesystemService
	AdminTaskMetrics                AdminTaskMetricsService
	AdminTasks                      AdminTaskService
	AdminTaskHistory                AdminTaskHistoryService
	Devices                         DeviceLoginService
	Sessions                        SessionService
	// PluginLaunch mints the plugin access cookie token (*handlers.AuthHandler).
	PluginLaunch          PluginLaunchService
	OAuth                 OAuthService
	BucketRateLimit       func(string) func(http.Handler) http.Handler
	Markers               MarkerService
	AdminSections         AdminSectionService
	AdminPolicy           AdminPolicyService
	AdminCollections      AdminCollectionService
	AdminCollectionGroups AdminCollectionGroupService
	WebhookSync           WebhookSyncService
	SubtitleProviders     SubtitleProviderStatusService
	SubtitleAI            SubtitleAIStatusService
	SubtitleReads         SubtitleReadService
	SubtitleDownloads     SubtitleDownloadService
	SubtitleUploads       SubtitleUploadService
	AdminSettingsWrite    AdminSettingsWriteService
	PluginContent         PluginContentService
	SubtitleAICancel      SubtitleAICancelService
	SubtitleAICreate      SubtitleAICreateService
	Playback              PlaybackService
	PlaybackMedia         *PlaybackMediaHandlers
	// Auth is the bearer/API-key gate shared with the v1 router.
	Auth *apimw.AuthMiddleware
	// ViewerAccess resolves the declared profile into a viewer scope.
	ViewerAccess *apimw.ViewerAccessMiddleware
	// ActingAdmin is the admin-through-primary-profile gate.
	ActingAdmin func(http.Handler) http.Handler
	// PermissionGates maps a permission name (policy.Permission* constants)
	// to the gate that enforces it.
	PermissionGates map[string]func(http.Handler) http.Handler
	// DemoSettings reads the demo.enabled setting; nil means demo mode is
	// never on.
	DemoSettings apimw.DemoSettingsReader
	// RateLimit is the generic authenticated-route limiter.
	RateLimit func(http.Handler) http.Handler
	// CursorSecret keys pagination cursors. It must be shared by every replica
	// (the JWT secret is); empty means a per-process random key.
	CursorSecret []byte

	// The services the pilot operations call. Each is the same value the
	// corresponding v1 handler calls, so the two surfaces read and write one
	// store. A nil service makes its operations answer 503
	// dependency_unavailable rather than disappear.

	// Accounts answers setup status and the current account
	// (*handlers.AuthHandler).
	Accounts AccountService
	// Progress lists watch progress (*handlers.ProgressHandler).
	Progress          ProgressService
	ProgressBootstrap ProgressBootstrapService
	// History lists and removes watch history
	// (*handlers.PersonalDataHandler).
	History        HistoryService
	DeviceSettings DeviceSettingsService
	// Watch answers watch detail and marks items watched
	// (*handlers.ItemsHandler).
	Watch WatchService
	// Profiles applies profile updates (*handlers.ProfileHandler).
	Profiles ProfileService
	// Libraries answers which library identifiers exist
	// (*catalog.FolderRepository); updateProfile validates an allowlist
	// against it so an unknown id is a 422 rather than a foreign-key 500.
	Libraries LibraryService
	// AdminUsers lists accounts for administrators (*handlers.AdminHandler).
	AdminUsers           AdminUserService
	AdminAccounts        AdminAccountService
	AdminAccountActivity AdminAccountActivityService
	AdminAccountSettings AdminAccountSettingsService
	AdminAccessGroups    AdminAccessGroupService
	// AdminPlaybackHistory pages the finalized playback log for administrators (*handlers.AdminHandler).
	AdminPlaybackHistory AdminPlaybackHistoryService
	// SettingsContract answers the settings capability document
	// (*handlers.SettingValuesHandler).
	SettingsContract SettingsContractService
	// Settings answers overlay defaults and device-scoped settings
	// (*handlers.SettingsHandler).
	Settings SettingsService
	// PluginSettings answers per-installation user plugin settings
	// (*handlers.PluginHandler).
	PluginSettings PluginSettingsService
	// SettingValues answers the explicit and effective setting values of the
	// settings contract (*handlers.SettingValuesHandler).
	SettingValues SettingValuesService
	// AudioPreferences reads and writes per-series audio preferences
	// (*handlers.AudioPrefHandler).
	AudioPreferences AudioPreferenceService
	// LibraryPlaybackPreferences reads and writes per-library playback
	// preferences (*handlers.LibraryPlaybackPrefHandler).
	LibraryPlaybackPreferences LibraryPlaybackPreferenceService
	// SubtitlePreferences reads and writes per-series subtitle preferences
	// (*handlers.SubtitlePrefHandler).
	SubtitlePreferences SubtitlePreferenceService
	// LibraryAdmin manages libraries for administrators
	// (*handlers.LibraryHandler).
	LibraryAdmin LibraryAdminService
	LibraryJobs  LibraryJobService
	// LibrarySections answers a library's sections to viewers
	// (*handlers.SectionHandler).
	LibrarySections LibrarySectionService
	// LibraryCollections answers a library's collections to viewers
	// (*handlers.LibraryCollectionHandler).
	LibraryCollections LibraryCollectionService
	// Calendar answers the airing calendar (*handlers.CalendarHandler).
	Calendar CalendarService
	// HomeDismissals records and clears home-card dismissals
	// (*handlers.HomeDismissalHandler).
	HomeDismissals HomeDismissalService
	// HomeSections answers the home page to viewers
	// (*handlers.SectionHandler).
	HomeSections HomeSectionService
	// Recipes answers the section recipe gallery (*handlers.RecipeHandler).
	Recipes RecipeService
	// PersonalLists reads and edits a profile's favorites
	// (*handlers.PersonalDataHandler).
	PersonalLists PersonalListService
	// Ratings reads and edits a profile's ratings (*handlers.RatingsHandler).
	Ratings RatingService
	// Recommendations answers the profile-scoped recommendation reads
	// (*handlers.RecommendationsHandler).
	Recommendations RecommendationService
	// ProfileSections reads and writes a profile's home-row overrides
	// (*handlers.SectionHandler).
	ProfileSections ProfileSectionService
	// SectionFlags reads the profile-facing sections settings
	// (*handlers.SectionSettingsHandler).
	SectionFlags SectionFlagService
	// Requests serves media requests and the discovery surface
	// (*requests.Service, the value *handlers.RequestsHandler wraps).
	AdminSubtitleInspection            AdminSubtitleInspectionService
	AdminRequests                      handlers.RequestService
	Requests                           MediaRequestService
	RequestLifecycle                   RequestLifecycleService
	WatchProviders                     WatchProviderService
	HistoryImports                     HistoryImportService
	AdminHistoryImports                AdminHistoryImportService
	AdminSubtitleList                  AdminSubtitleListService
	AdminSubtitleMetadata              AdminSubtitleMetadataService
	AdminSubtitleBytes                 AdminSubtitleBytesService
	AdminSubtitleDelete                AdminSubtitleDeleteService
	AdminAPIKeys                       AdminAPIKeyService
	PersonalAPIKeys                    PersonalAPIKeyService
	PolicyCapability                   PolicyCapabilityService
	Branding                           BrandingService
	ThemeOverrides                     ThemeOverrideService
	AdminInviteCodes                   AdminInviteCodeService
	Invitations                        InvitationService
	ThemeCatalog                       ThemeCatalogService
	AdminSubtitleProviderConfiguration AdminSubtitleProviderConfigurationService
	// PersonalCollections manages a profile's own collections and groups
	// (*handlers.CollectionHandler).
	PersonalCollections PersonalCollectionService
	// CollectionImports creates synced collections from external lists and
	// searches MDBList (*handlers.UserCollectionImportHandler).
	CollectionImports CollectionImportService
	// CatalogAccess resolves a viewer's access filter (*handlers.ItemsHandler);
	// every catalog read needs it alongside its own seam.
	CatalogAccess CatalogAccessService
	// CatalogBrowse pages and facets the catalog (*handlers.CatalogHandler).
	CatalogBrowse CatalogBrowseService
	// CatalogItems answers item, season, and episode reads
	// (*handlers.CatalogResourceHandler).
	CatalogItems CatalogItemService
	// CatalogTrailers answers the trailer capability and refresh action
	// (*handlers.ItemsHandler).
	CatalogTrailers CatalogTrailerService
	// MetadataAI answers the metadata AI capability and the on-view
	// translation action (*handlers.MetadataAIHandler).
	MetadataAI MetadataAIService
	// People answers person search, detail, and refresh
	// (*handlers.PeopleHandler).
	People PeopleService
	// LiteraryWorks answers literary works (*handlers.LiteraryWorkHandler).
	LiteraryWorks LiteraryWorkService

	// bodyReadTimeout overrides BodyReadTimeout; tests use it to exercise the
	// 408 boundary without waiting for the production deadline.
	bodyReadTimeout time.Duration
	// testRegister registers probe operations; tests only.
	testRegister func(*Registry)
}

// sealedHandler is what NewHandler hands out: the finished router behind an
// unexported field and a ServeHTTP method, nothing else, so no assertion or
// type switch recovers a registration surface from it (see
// docs/architecture/api-contract.md, "Legacy native route inventory"). Do not
// embed http.Handler here: embedding exports the field and promotes its
// methods.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

// NewHandler builds the v2 listener. It returns a sealed http.Handler, never
// the chi surface; the route inventory checks this shape.
func NewHandler(deps Dependencies) http.Handler {
	return sealedHandler{h: newChiRouter(deps)}
}

// newChiRouter is the v2 listener's registration surface. The route inventory
// walks this function: the router is handed to the Huma adapter exactly once,
// and every operation registered through the Registry is described by
// contracts/api/v2/openapi.json rather than by an inventory row.
func newChiRouter(deps Dependencies) chi.Router {
	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(observe)
	r.Use(dropDelegationPattern)
	r.Use(joinPreconditionFields)
	r.Use(rejectMalformedDeviceHeader)
	r.Use(withRequest)
	r.NotFound(notFound)

	api := humachi.New(r, humaConfig())
	var routeSnapshot []streamtelemetry.WalkedRoute
	if deps.ObserveRoutes != nil {
		api = huma.NewAPI(humaConfig(), &routeSnapshotAdapter{Adapter: api.Adapter(), routes: &routeSnapshot})
	}
	api.UseMiddleware(bufferStructuredResponse, observeOperation, defaultHeaders, deprecationHeaders, classGate(deps), observeIdentity, normalizeAccept, encodingGuard, mediaTypeGuard, queryGuard)

	reg := &Registry{api: api, deps: deps}
	registerAll(reg)
	if deps.testRegister != nil {
		deps.testRegister(reg)
	}
	// After registration: the 405 answer names the methods the registry
	// declared for the matched path, which is only known once every operation
	// is in.
	r.MethodNotAllowed(reg.methodNotAllowed)
	if deps.ObserveRoutes != nil {
		deps.ObserveRoutes(routeSnapshot)
	}

	return r
}

// humaConfig is the framework configuration the contract ratifies. Nothing
// here is a Huma default accepted implicitly; each choice is locked by a test
// in router_test.go.
func humaConfig() huma.Config {
	return huma.Config{
		OpenAPI: &huma.OpenAPI{
			OpenAPI: "3.1.0",
			Info:    &huma.Info{Title: "Silo API", Version: fmt.Sprintf("%d", APIMajor)},
			Components: &huma.Components{
				Schemas: huma.NewMapRegistry("#/components/schemas/", nativeSchemaName),
				SecuritySchemes: map[string]*huma.SecurityScheme{
					securitySchemeBearer: {
						Type:        "http",
						Scheme:      "bearer",
						Description: "A session token or an API key in the Authorization header. The server decides which it holds.",
					},
				},
			},
			// The document lists only the request media types the listener
			// accepts; the generator and the served router share this
			// config, so the artifact and the runtime document agree.
			OnAddOperation: []huma.AddOpFunc{documentAcceptedRequestMediaTypes},
		},
		// Built-in spec, docs and schema routes are disabled: Silo serves the
		// committed artifact (getOpenAPIDocument) and bundled viewer itself.
		OpenAPIPath: "",
		DocsPath:    "",
		SchemasPath: "",
		// The "json" alias is what Huma's marshaller resolves the
		// application/problem+json suffix to; normalizeAccept keeps a bare
		// `Accept: json` from selecting it.
		Formats:       map[string]huma.Format{mediaTypeJSON: huma.DefaultJSONFormat, "json": huma.DefaultJSONFormat},
		DefaultFormat: mediaTypeJSON,
		// An unacceptable Accept is 406, never a silent fallback.
		NoFormatFallback: true,
		// An undeclared query parameter is a validation failure.
		RejectUnknownQueryParameters: true,
		// The schema-link transformer is absent: no $schema member, no links.
		Transformers: []huma.Transformer{problemTransformer},
	}
}

// documentAcceptedRequestMediaTypes drops every request media type the
// framework documented that mediaTypeGuard would answer with 415. Huma
// documents a RawBody member as application/octet-stream even when the
// operation also declares a structured Body (updateProfile reads the raw
// document only for its omitted-versus-null rule), and the listener accepts
// application/json and, on an operation declaring a multipart form,
// multipart/form-data alone. A multipart body is always required: the form
// is the whole request, and an absent one is the 415 the guard documents. It runs after Huma has built the request body and
// before the operation reaches the document, so the schema Huma derived for
// validation is untouched.
func documentAcceptedRequestMediaTypes(oapi *huma.OpenAPI, op *huma.Operation) {
	if op.RequestBody == nil {
		return
	}
	for mediaType := range op.RequestBody.Content {
		if !requestMediaTypeOK(mediaType) {
			delete(op.RequestBody.Content, mediaType)
		}
	}
	if media := op.RequestBody.Content[mediaTypeMultipart]; media != nil {
		op.RequestBody.Required = true
		nameMultipartForm(oapi, op, media)
	}
}

// nameMultipartForm moves the form schema Huma derived for a multipart
// operation into components under the form type's name and leaves a
// reference in its place, as every other request schema is named. The
// framework's own "file required" check reads the inline schema, so the
// operation checks the part itself.
func nameMultipartForm(oapi *huma.OpenAPI, op *huma.Operation, media *huma.MediaType) {
	name, _ := op.Metadata[metaFormSchema].(string)
	if name == "" || media.Schema == nil || media.Schema.Ref != "" {
		return
	}
	schemas := oapi.Components.Schemas.Map()
	if prev, taken := schemas[name]; taken && prev != media.Schema {
		panic(fmt.Sprintf("apiv2: %s: form schema name %q is already registered", op.OperationID, name))
	}
	media.Schema.AdditionalProperties = false
	schemas[name] = media.Schema
	media.Schema = &huma.Schema{Ref: "#/components/schemas/" + name}
}

// requestID exposes the canonical request ID. Under the API listener,
// apimw.RequestID has already stored a server-generated one in the context;
// it is reused verbatim so the request log, activity log and the problem
// `instance` all name the same request. Standalone (tests), one is minted
// with the same generator. A client-supplied X-Request-Id is never adopted
// anywhere in either chain.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chimw.GetReqID(r.Context())
		if id == "" {
			id = apimw.NewRequestID()
			r = r.WithContext(context.WithValue(r.Context(), chimw.RequestIDKey, id))
		}
		// Set through the map to keep the contract's spelling (X-Request-ID);
		// Header.Set would canonicalize it.
		w.Header()[RequestIDHeader] = []string{id}
		next.ServeHTTP(w, r)
	})
}

// joinPreconditionFields folds repeated If-Match and If-None-Match header
// lines into one comma-separated value (RFC 9110 5.3: a recipient may combine
// list-based fields in order). Huma binds a header input from Header.Get,
// which returns the first line only, so without this a second line would be
// silently dropped and the precondition evaluated against half the list.
func joinPreconditionFields(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, field := range []string{ifMatchField, ifNoneMatchField} {
			vs, present := r.Header[textproto.CanonicalMIMEHeaderKey(field)]
			if !present {
				continue
			}
			joined := strings.Join(vs, ", ")
			if strings.TrimSpace(joined) == "" {
				// Huma binds a header to a string, where a present but
				// empty field is indistinguishable from an absent one. The
				// RFC #-list rule makes the empty field an empty list that
				// matches nothing (412), not a missing precondition (428),
				// so it is rewritten to a spelling the parser sees as empty.
				joined = emptyETagList
			}
			r.Header.Set(field, joined)
		}
		next.ServeHTTP(w, r)
	})
}

// dropDelegationPattern removes the API listener's wildcard pattern from the
// chi route context so the request logger and activity log see the operation
// path, not "/api/v2/*" joined with it.
func dropDelegationPattern(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			if n := len(rctx.RoutePatterns); n > 0 && rctx.RoutePatterns[n-1] == DelegationPattern {
				rctx.RoutePatterns = rctx.RoutePatterns[:n-1]
			}
		}
		next.ServeHTTP(w, r)
	})
}

// bufferResponse holds the response until the operation has finished, then
// writes it in one go. That is what makes three contract rules hold:
//
//   - a panic anywhere, including Huma's own marshal failure after the status
//     would already have been sent, becomes the internal_error problem with no
//     detail leakage;
//   - a success body is never written once the client has gone away (net/http
//     cancels the request context on disconnect);
//   - a problem — including the 408 for a body-read timeout, which net/http
//     also reports as a canceled context — is still delivered.
//
// Structured responses are bounded JSON documents, so buffering them is
// cheap; raw media and streams are not served through Huma.
// bufferStructuredResponse confines buffering and JSON panic recovery to Huma
// operations. Raw registrations preserve the original streaming writer.
func bufferStructuredResponse(ctx huma.Context, next func(huma.Context)) {
	r, w := humachi.Unwrap(ctx)
	bufferResponse(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next(humachi.NewContext(ctx.Operation(), r, w))
	})).ServeHTTP(w, r)
}

func bufferResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bw := &bufferedWriter{w: w, ctx: r.Context(), retirement: make(http.Header)}
		r = r.WithContext(context.WithValue(r.Context(), retirementHeadersKey{}, bw.retirement))
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
					panic(rec)
				}
				slog.ErrorContext(r.Context(), "apiv2 handler panic",
					"component", "apiv2",
					"request_id", requestIDFrom(r.Context()),
					"panic", fmt.Sprint(rec),
					"stack", string(debug.Stack()))
				bw.discard()
				writeProblem(bw, r, NewProblem(TypeInternalError, "An unexpected error occurred."))
			}
			bw.flush()
		}()
		next.ServeHTTP(bw, r)
	})
}

type bufferedWriter struct {
	w          http.ResponseWriter
	ctx        context.Context
	status     int
	body       bytes.Buffer
	flushed    bool
	retirement http.Header
}

func (b *bufferedWriter) Header() http.Header { return b.w.Header() }

func (b *bufferedWriter) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// Unwrap lets huma.SetReadDeadline and http.ResponseController reach the
// connection.
func (b *bufferedWriter) Unwrap() http.ResponseWriter { return b.w }

// discard drops the failed response and restores only the operation's declared
// retirement values, captured before downstream middleware can replace them.
func (b *bufferedWriter) discard() {
	b.status = 0
	b.body.Reset()
	h := b.w.Header()
	requestID := h[RequestIDHeader] //nolint:staticcheck // exact contract spelling
	clear(h)
	if len(requestID) > 0 {
		h[RequestIDHeader] = requestID //nolint:staticcheck // exact contract spelling
	}
	maps.Copy(h, b.retirement)
}

func (b *bufferedWriter) flush() {
	if b.flushed {
		return
	}
	b.flushed = true
	if b.status == 0 {
		b.status = http.StatusOK
	}
	if b.ctx.Err() != nil && b.status < 400 {
		// The client is gone; a success body has nobody to read it.
		return
	}
	if b.status == http.StatusNotModified || b.status == http.StatusNoContent {
		// No body travels with these statuses, so no representation header
		// describes one; Huma negotiates Content-Type before it knows. A
		// HEAD body is not cleared here: net/http's ResponseWriter discards
		// it on a real connection (after counting it for Content-Length),
		// so the listener leaves that to the server.
		b.w.Header().Del("Content-Type")
		b.body.Reset()
	}
	b.w.WriteHeader(b.status)
	_, _ = b.w.Write(b.body.Bytes())
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, NewProblem(TypeNotFound, "No operation is registered at this path."))
}

// methodNotAllowed answers a matched path with an unsupported method. chi does
// not hand the allowed set to a custom handler, so it is recomputed from the
// registry's declared rows: RFC 9110 requires Allow on a 405.
func (reg *Registry) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	p := NewProblem(TypeMethodNotAllowed, "The method is not supported at this path.")
	methods := reg.AllowedMethods(r.URL.Path)
	if len(methods) == 0 {
		methods = pluginContentAllowedMethods(r.URL.Path)
	}
	if allow := strings.Join(methods, ", "); allow != "" {
		p = p.WithHeader("Allow", allow)
	}
	writeProblem(w, r, p)
}

// The pilot services are narrow slices of the v1 handlers: each method is
// the business logic the v1 handler runs after parsing its request, so v2
// cannot drift from v1 in what it reads or writes. A *handlers.APIError is
// mapped onto the problem type of its status; any other error is 500.

// AccountService is the slice of *handlers.AuthHandler the account and setup
// operations use.
type AccountService interface {
	AccountPasswordCapability(context.Context, *auth.Claims, string) (handlers.AccountPasswordCapabilityView, error)
	AuthorizePasswordChange(context.Context, *auth.Claims, string) error
	ChangePassword(context.Context, *auth.Claims, string, string) error
	NeedsSetup(ctx context.Context) (bool, error)
	SetupWizardCompleted(ctx context.Context) (bool, error)
	CurrentUser(ctx context.Context, claims *auth.Claims) (handlers.UserView, error)
}

// ProgressService is the slice of *handlers.ProgressHandler the progress
// operations use.
type ProgressService interface {
	ListProgressPage(ctx context.Context, userID int, profileID string, status string, libraryID int, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, bool, error)
	SyncProgress(ctx context.Context, userID int, profileID string, updates []handlers.ProgressSyncUpdate) ([]handlers.ProgressSyncResultView, error)
}

// ProfileService is the slice of *handlers.ProfileHandler the profile
// operations use.
type ProfileService interface {
	ListProfiles(ctx context.Context, userID int) (handlers.ProfileListView, error)
	CreateProfile(ctx context.Context, cmd handlers.ProfileCreateCommand) (handlers.ProfileView, error)
	UpdateProfile(ctx context.Context, cmd handlers.ProfileUpdateCommand) (handlers.ProfileView, error)
	DeleteProfile(ctx context.Context, cmd handlers.ProfileDeleteCommand) error
	ListHouseholdSessions(ctx context.Context, q handlers.HouseholdSessionsQuery) ([]handlers.PlaybackSessionView, error)
	VerifyPIN(ctx context.Context, cmd handlers.ProfileVerifyPINCommand) (handlers.ProfileVerification, error)
	UploadAvatar(ctx context.Context, up handlers.ProfileAvatarUpload) (handlers.ProfileView, error)
	DeleteAvatar(ctx context.Context, userID int, profileID string) (handlers.ProfileView, error)
}

// ProfileSectionService is the slice of *handlers.SectionHandler the
// profile section-override operations use.
type ProfileSectionService interface {
	ListProfileOverrides(ctx context.Context, q handlers.SectionOverridesQuery) ([]userstore.SectionOverride, error)
	SaveProfileOverrides(ctx context.Context, q handlers.SectionOverridesQuery, writes []handlers.SectionOverrideWrite) error
	ResetProfileOverrides(ctx context.Context, q handlers.SectionOverridesQuery) error
	ResolveProfileSectionSettings(ctx context.Context, userID int, profileID, scope string, libraryID *int, filter mediacatalog.AccessFilter) ([]sections.ResolvedSection, error)
}

// SectionFlagService is the slice of *handlers.SectionSettingsHandler
// getProfileSectionFlags uses.
type SectionFlagService interface {
	AllowProfileCustomSections(ctx context.Context) bool
}

// LibraryService is the slice of *catalog.FolderRepository updateProfile
// uses to validate a library allowlist before the store sees it.
type LibraryService interface {
	// ExistingIDs returns the subset of ids that name a library.
	ExistingIDs(ctx context.Context, ids []int) ([]int, error)
}

// AdminUserService is the slice of *handlers.AdminHandler listAdminUsers uses.
type AdminUserService interface {
	ListAdminUsersPage(ctx context.Context, afterID, limit int, identity string) ([]handlers.AdminUserView, bool, error)
}

// SettingsContractService is the slice of *handlers.SettingValuesHandler
// getSettingsContractCapabilities uses.
type SettingsContractService interface {
	Capabilities(ctx context.Context) (handlers.SettingsCapabilitiesView, error)
}

// SettingsService is the slice of *handlers.SettingsHandler the overlay and
// subtitle-appearance operations use.
type SettingsService interface {
	OverlayConfig(ctx context.Context) handlers.OverlayConfigView
	EffectiveSubtitleAppearance(ctx context.Context, userID int, profileID string, device handlers.DeviceMetadata) (handlers.EffectiveSubtitleAppearanceView, error)
	SetDeviceSetting(ctx context.Context, cmd handlers.DeviceSettingCommand, value string) error
	DeleteDeviceSetting(ctx context.Context, cmd handlers.DeviceSettingCommand) error
}

// PluginSettingsService is the slice of *handlers.PluginHandler the plugin
// settings operations use.
type PluginSettingsService interface {
	ListUserPluginSettings(ctx context.Context) ([]handlers.PluginUserSettingsView, error)
	GetUserPluginSettings(ctx context.Context, userID, installationID int) (handlers.PluginUserSettingsDetailView, error)
	SetUserPluginSettings(ctx context.Context, userID, installationID int, values map[string]string) error
}

// SettingValuesService is the slice of *handlers.SettingValuesHandler the
// setting value operations use: the request-free core of the v1 values API.
type SettingValuesService interface {
	ContractRevision() int
	GetSettingValue(ctx context.Context, userID int, req handlers.SettingIdentityRequest) (handlers.SettingValueView, error)
	ListSettingValues(ctx context.Context, userID int, keys []string, req handlers.SettingIdentityRequest) ([]handlers.ExplicitSettingValueView, error)
	SetSettingValue(ctx context.Context, userID int, req handlers.SettingIdentityRequest, value json.RawMessage) (handlers.SettingValueView, error)
	DeleteSettingValue(ctx context.Context, userID int, req handlers.SettingIdentityRequest) error
	SetNavigationShortcut(ctx context.Context, userID int, profileID string, item json.RawMessage, present bool) (handlers.SettingValueView, error)
	ResolveEffectiveSettings(ctx context.Context, userID int, q handlers.EffectiveSettingsQuery) ([]handlers.EffectiveSettingValueView, error)
	ResolveEffectiveSettingContexts(ctx context.Context, userID int, q handlers.EffectiveSettingsQuery, contexts []handlers.EffectiveContextRequest) ([]handlers.EffectiveSettingContextView, error)
}

// AudioPreferenceService is the slice of *handlers.AudioPrefHandler the
// audio preference operations use.
type AudioPreferenceService interface {
	GetAudioPreferenceCanonical(ctx context.Context, userID int, profileID, seriesID string) (userstore.AudioPreference, error)
	SetAudioPreference(ctx context.Context, userID int, pref userstore.AudioPreference) error
	DeleteAudioPreference(ctx context.Context, userID int, profileID, seriesID string) error
}

// SubtitlePreferenceService is the slice of *handlers.SubtitlePrefHandler the
// subtitle preference operations use.
type SubtitlePreferenceService interface {
	GetSubtitlePreferenceCanonical(ctx context.Context, userID int, profileID, seriesID string) (userstore.SubtitlePreference, error)
	SetSubtitlePreferenceCanonical(ctx context.Context, userID int, pref userstore.SubtitlePreference) error
	DeleteSubtitlePreference(ctx context.Context, userID int, profileID, seriesID string) error
}

// LibraryPlaybackPreferenceService is the slice of
// *handlers.LibraryPlaybackPrefHandler the library preference operations use.
type LibraryPlaybackPreferenceService interface {
	ListLibraryPlaybackPreferencesCanonical(ctx context.Context, userID int, profileID string) ([]userstore.LibraryPlaybackPreference, error)
	PatchLibraryPlaybackPreference(ctx context.Context, userID int, profileID string, libraryID int, patch handlers.LibraryPlaybackPrefPatch) error
	DeleteLibraryPlaybackPreference(ctx context.Context, userID int, profileID string, libraryID int) error
}

// LibraryAdminService is the slice of *handlers.LibraryHandler the library
// management operations use. Every method returns the view its v1 handler
// writes and an *handlers.APIError on failure.
type LibraryAdminService interface {
	ListLibraries(ctx context.Context) ([]handlers.LibraryView, error)
	CreateLibrary(ctx context.Context, req handlers.LibraryCreateRequest) (handlers.LibraryView, error)
	UpdateLibrary(ctx context.Context, id, userID int, req handlers.LibraryUpdateRequest) (handlers.LibraryView, error)
	DeleteLibrary(ctx context.Context, id, userID int) (*models.AdminJob, error)
	CheckLibraryMount(ctx context.Context, id int) (handlers.LibraryMountCheckView, error)
	ConfirmEmptyRootCleanup(ctx context.Context, id int) error
	ListMetadataMatchQueues(ctx context.Context) ([]handlers.MetadataMatchQueueStatusView, error)
	LibraryProviderDefaults(ctx context.Context, libraryType string) (map[string][]handlers.ChainLevelEntryView, error)
	ReorderLibraries(ctx context.Context, entries []mediacatalog.FolderReorderEntry) error
	ListLibraryRoots(ctx context.Context, libraryID int, state, search string, limit, offset int) ([]handlers.LibraryRootView, int, error)
	SetRootOverride(ctx context.Context, userID int, req handlers.RootOverrideUpsertRequest) error
	DeleteRootOverride(ctx context.Context, req handlers.RootOverrideDeleteRequest) error
	ListSkippedRoots(ctx context.Context, search string, limit, offset int) ([]handlers.SkippedRootView, error)
	ListStaleIDs(ctx context.Context, search string, limit, offset int) ([]handlers.StaleMediaIDView, error)
	RematchStaleID(ctx context.Context, contentID string) error
	ListUnmatchedItems(ctx context.Context, search string, limit, offset int) ([]handlers.UnmatchedItemView, int, error)
	GetMetadataMatchQueue(ctx context.Context, id, limit, offset int) (handlers.MetadataMatchQueueDetailView, error)
	RetryMetadataMatchQueue(ctx context.Context, id int) (handlers.MetadataMatchQueueActionView, error)
	CancelMetadataMatchQueue(ctx context.Context, id int) (handlers.MetadataMatchQueueActionView, error)
	RefreshLibraryMetadata(ctx context.Context, id, userID int, mode adminjob.LibraryRefreshMode) (*models.AdminJob, error)
	UploadLibraryPoster(ctx context.Context, id int, contentType string, data []byte) (handlers.LibraryView, error)
	DeleteLibraryPoster(ctx context.Context, id int) error
	LibraryProviders(ctx context.Context, id int) (map[string][]handlers.ChainLevelEntryView, error)
	SetLibraryProviders(ctx context.Context, id int, levels map[string][]handlers.ProviderChainEntryInput) error
}

// LibrarySectionService is the slice of *handlers.SectionHandler the
// viewer-facing library section reads use.
type LibrarySectionService interface {
	LibraryLayout(ctx context.Context, libraryID int) (handlers.SectionLayoutView, error)
	LibrarySections(ctx context.Context, libraryID int, viewer handlers.SectionViewer) (handlers.SectionsView, error)
	LibrarySectionItems(ctx context.Context, libraryID int, sectionID string, viewer handlers.SectionViewer) (handlers.SectionView, error)
}

// LibraryCollectionService is the slice of *handlers.LibraryCollectionHandler
// the viewer-facing library collection reads use.
type LibraryCollectionService interface {
	LibraryCollectionsTab(ctx context.Context, libraryID, userID int, profileID string) (handlers.LibraryCollectionTabView, error)
	LibraryUserCollections(ctx context.Context, libraryID, userID int, profileID string) ([]usercollections.ServerVisibleCollection, error)
}

// CalendarService is the slice of *handlers.CalendarHandler getCalendar uses.
type CalendarService interface {
	Calendar(ctx context.Context, q handlers.CalendarQuery, access mediacatalog.AccessFilter) (handlers.CalendarView, error)
}

// HomeDismissalService is the slice of *handlers.HomeDismissalHandler the
// dismissal operations use.
type HomeDismissalService interface {
	DismissHomeItem(ctx context.Context, cmd handlers.HomeDismissalCommand) error
	UndismissHomeItem(ctx context.Context, userID int, profileID, surface, itemID string) error
}

// HomeSectionService is the slice of *handlers.SectionHandler the
// viewer-facing home reads use.
type HomeSectionService interface {
	HomeLayout(ctx context.Context) (handlers.SectionLayoutView, error)
	HomeSections(ctx context.Context, viewer handlers.SectionViewer) (handlers.SectionsView, error)
	HomeSectionItems(ctx context.Context, sectionID string, viewer handlers.SectionViewer) (handlers.SectionView, error)
}

// RecipeService is the slice of *handlers.RecipeHandler the section recipe
// gallery reads use.
type RecipeService interface {
	Recipes() []handlers.RecipeCategoryView
	RecipeCandidates(ctx context.Context, recipeType string) ([]handlers.Candidate, error)
}

// PersonalListService is the slice of *handlers.PersonalDataHandler the
// favorites and watchlist operations use. Every method acts as the viewer's profile and
// returns an *handlers.APIError on failure.
type PersonalListService interface {
	// ListFavoritesPage answers at most limit entries of the profile's
	// favorites ordered by (added_at DESC, media_item_id DESC) strictly after
	// the key (nil = from the newest), and the cards of the entries the
	// viewer may see, in the same order.
	ListFavoritesPage(ctx context.Context, viewer handlers.PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.Favorite, []handlers.CollectionItemView, error)
	// GetFavorite answers the entry of an item the viewer may see; found is
	// false when the item is not a favorite.
	GetFavorite(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) (entry userstore.Favorite, found bool, err error)
	AddFavorite(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) error
	RemoveFavorite(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) error
	// ListWatchlistPage answers at most limit entries of the profile's
	// watchlist ordered by (added_at DESC, media_item_id DESC) strictly after
	// the key (nil = from the newest), and the cards of the entries the
	// viewer may see (fully-watched series hidden), in the same order.
	ListWatchlistPage(ctx context.Context, viewer handlers.PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.WatchlistEntry, []handlers.CollectionItemView, error)
	// GetWatchlistEntry answers the entry of an item the viewer may see;
	// found is false when the item is not on the watchlist.
	GetWatchlistEntry(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) (entry userstore.WatchlistEntry, found bool, err error)
	AddToWatchlist(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) error
	RemoveFromWatchlist(ctx context.Context, viewer handlers.PersonalListViewer, itemID string) error
}

// RatingService is the slice of *handlers.RatingsHandler the ratings
// operations use.
type RatingService interface {
	// ListRatingsPage answers at most limit of the profile's ratings ordered
	// by (rated_at DESC, media_item_id DESC) strictly after the key (nil =
	// from the most recently rated).
	ListRatingsPage(ctx context.Context, userID int, profileID string, after *mediacatalog.RatingKey, limit int) ([]mediacatalog.UserRating, error)
	// GetRating answers the profile's rating of the item; found is false
	// when the profile has not rated it.
	GetRating(ctx context.Context, userID int, profileID, itemID string) (rating mediacatalog.UserRating, found bool, err error)
	// SetRating records a validated rating of an item the access filter
	// admits; an item outside it is a 404 error.
	SetRating(ctx context.Context, userID int, profileID, itemID string, access mediacatalog.AccessFilter, rating int) error
	DeleteRating(ctx context.Context, userID int, profileID, itemID string) error
}

// RecommendationService is the slice of *handlers.RecommendationsHandler the
// viewer-facing recommendation reads use. Every method returns the cards
// (or rows of cards) the discover page renders and an *handlers.APIError on
// failure.
type RecommendationService interface {
	BecauseWatchedCards(ctx context.Context, userID int, profileID, itemID string, limit int, filter mediacatalog.AccessFilter) ([]handlers.SectionItemView, error)
	PopularCards(ctx context.Context, userID int, profileID string, days, limit int, filter mediacatalog.AccessFilter) ([]handlers.SectionItemView, error)
	RecentlyAddedCards(ctx context.Context, userID int, profileID string, days, limit int, filter mediacatalog.AccessFilter) ([]handlers.SectionItemView, error)
	ForYouMainCards(ctx context.Context, userID int, profileID string, limit int, filter mediacatalog.AccessFilter) (handlers.DiscoverRowView, error)
	ForYouRowCards(ctx context.Context, userID int, profileID string, limit int, filter mediacatalog.AccessFilter) ([]handlers.DiscoverRowView, error)
	Discover(ctx context.Context, userID int, profileID string, filter mediacatalog.AccessFilter) (handlers.DiscoverView, error)
	Section(ctx context.Context, userID int, profileID, kind, key string, limit int, filter mediacatalog.AccessFilter) (handlers.SectionDetailView, error)
	SimilarCards(ctx context.Context, userID int, profileID, itemID string, limit int, filter mediacatalog.AccessFilter) ([]handlers.SectionItemView, error)
	SimilarUsersCards(ctx context.Context, userID int, profileID string, limit int, filter mediacatalog.AccessFilter) ([]handlers.SectionItemView, error)
	TasteProfile(ctx context.Context, userID int, profileID string) recommendations.TasteProfileSummary
	TasteSeedItems(ctx context.Context, userID int, profileID string, filter mediacatalog.AccessFilter, limit, offset int) (items []handlers.SectionItemView, candidates int, err error)
	SubmitTasteSeed(ctx context.Context, userID int, profileID string, itemIDs []string, filter mediacatalog.AccessFilter) (int, error)
	WatchTonight(ctx context.Context, userID int, profileID string, filter mediacatalog.AccessFilter, limit int) (handlers.WatchTonightView, error)
	WatchTonightCards(ctx context.Context, userID int, profileID string, filter mediacatalog.AccessFilter, mode string, genres []string, excludeIDs map[string]struct{}, limit int) handlers.WatchTonightCardsView
}

// MediaRequestService is the slice of *requests.Service (the v1
// handlers.RequestService) the profile-facing request operations use.
type MediaRequestService interface {
	Search(ctx context.Context, viewer mediarequests.Viewer, query string, mediaType mediarequests.MediaType, page int) (*mediarequests.MediaPage, error)
	Discover(ctx context.Context, viewer mediarequests.Viewer, section string, page int) (*mediarequests.DiscoverySection, error)
	DiscoverAll(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverySection, error)
	GetDetail(ctx context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) (*mediarequests.MediaDetail, error)
	CreateRequest(ctx context.Context, viewer mediarequests.Viewer, input mediarequests.CreateRequestInput) (*mediarequests.Request, error)
	ListMine(ctx context.Context, viewer mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error)
	GetRequest(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	ListStudios(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	ListNetworks(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	ListGenres(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	BrowseStudio(ctx context.Context, viewer mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
	BrowseNetwork(ctx context.Context, viewer mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
	BrowseGenre(ctx context.Context, viewer mediarequests.Viewer, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
}

// CatalogAccessService is the slice of *handlers.ItemsHandler every catalog
// read uses to resolve the viewer's access filter.
type CatalogAccessService interface {
	ContextAccessFilter(ctx context.Context, opts handlers.AccessFilterOptions) (mediacatalog.AccessFilter, error)
}

// CatalogBrowseService is the slice of *handlers.CatalogHandler the browse,
// facet, and audiobook-group operations use.
type CatalogBrowseService interface {
	Browse(ctx context.Context, v handlers.ItemViewer, req mediacatalog.CatalogRequest, groupedByWork bool) (handlers.CatalogBrowseView, error)
	Filters(ctx context.Context, v handlers.ItemViewer, req mediacatalog.CatalogRequest, includeTechnical bool) (handlers.CatalogFiltersView, error)
	SearchFacet(ctx context.Context, v handlers.ItemViewer, req mediacatalog.CatalogRequest, facet, prefix string, limit int) (handlers.CatalogFacetSearchView, error)
	AudiobookGroups(ctx context.Context, v handlers.ItemViewer, query mediacatalog.AudiobookGroupsQuery) (handlers.AudiobookGroupsView, error)
}

// CatalogItemService is the slice of *handlers.CatalogResourceHandler the
// item, season, and episode reads use.
type CatalogItemService interface {
	ItemDetail(ctx context.Context, v handlers.ItemViewer, id string) (*mediacatalog.ItemDetail, error)
	ItemVersions(ctx context.Context, v handlers.ItemViewer, id string) ([]mediacatalog.FileVersion, error)
	MangaFiles(ctx context.Context, v handlers.ItemViewer, id string) (*mediacatalog.MangaSeriesFiles, error)
	ItemEpisodes(ctx context.Context, v handlers.ItemViewer, id string) ([]handlers.EpisodeView, error)
	SeriesSeasons(ctx context.Context, v handlers.ItemViewer, id string, includeArtwork bool) ([]handlers.SeasonView, error)
	SeriesSeason(ctx context.Context, v handlers.ItemViewer, id string, num int) (handlers.SeasonView, error)
	SeasonEpisodes(ctx context.Context, v handlers.ItemViewer, id string, num int) ([]handlers.EpisodeView, error)
}

// CatalogTrailerService is the slice of *handlers.ItemsHandler the trailer
// capability and refresh action use.
type CatalogTrailerService interface {
	TrailerRefreshCapability() handlers.TrailerRefreshCapabilityView
	RequestTrailersRefresh(ctx context.Context, userID int, contentID string, resolveAccess func() (mediacatalog.AccessFilter, error)) (handlers.TrailerRefreshView, error)
}

// MetadataAIService is the slice of *handlers.MetadataAIHandler the
// capability and on-view translation use.
type MetadataAIService interface {
	Status() handlers.MetadataAIStatusView
	TranslateOnView(ctx context.Context, filter mediacatalog.AccessFilter, contentID, targetLanguage string, requestedBy *int) (*translation.Job, error)
}

// PeopleService is the slice of *handlers.PeopleHandler the people
// operations use.
type PeopleService interface {
	SearchPeople(ctx context.Context, query string, limit int) ([]handlers.PersonView, error)
	Person(ctx context.Context, id int64) (handlers.PersonView, error)
	RefreshPerson(ctx context.Context, userID int, id int64) error
}

// LiteraryWorkService is the slice of *handlers.LiteraryWorkHandler the work
// read uses.
type LiteraryWorkService interface {
	Work(ctx context.Context, workID string, filter mediacatalog.AccessFilter) (*literaryworks.DetailResponse, error)
}

// unavailable is the fail-closed answer of an operation whose service is not
// wired, matching the gate rule for a missing gate.
func unavailable(what string) *Problem {
	return NewProblem(TypeDependencyUnavailable, "The "+what+" service is not available on this server.").WithRetryAfter(30)
}

// serviceProblem renders a service failure. A *handlers.APIError keeps the
// v1 decision (status and message); anything else is an internal error with
// no detail leaked.
func serviceProblem(err error) *Problem {
	var problem *Problem
	if errors.As(err, &problem) {
		return problem
	}
	var apiErr *handlers.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status >= 500 && apiErr.Status != http.StatusServiceUnavailable {
			return NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
		p := NewProblem(TypeForStatus(apiErr.Status), apiErr.Message)
		if apiErr.RetryAfter > 0 {
			p = p.WithRetryAfter(apiErr.RetryAfter)
		}
		return p
	}
	return NewProblem(TypeInternalError, "An unexpected error occurred.")
}

// OAuthService is the slice of *auth.OAuthHandler completeOAuthLogin uses.
type OAuthService interface {
	Complete(ctx context.Context, code string) (auth.OAuthCompletion, error)
	CallbackURL(prefix string, installID int) string
	Init(ctx context.Context, installID int, next, redirectURI string) (string, error)
	Callback(ctx context.Context, in auth.OAuthCallbackInput) string
}

// SessionService is the slice of *handlers.AuthHandler the login-session
// operations use.
type SessionService interface {
	Login(ctx context.Context, in handlers.LoginInput) (handlers.TokenPairView, error)
	Logout(ctx context.Context, claims *auth.Claims) error
	EndImpersonation(ctx context.Context, claims *auth.Claims) error
	ListProviders() []auth.LoginProviderInfo
	Refresh(ctx context.Context, refreshToken string) (handlers.RefreshedTokensView, error)
	ListSessionsPage(ctx context.Context, userID int, after *auth.SessionKey, limit int) ([]*models.AuthSession, bool, error)
	RevokeSession(ctx context.Context, sessionID string, userID int) error
	SetupInitialUser(ctx context.Context, in handlers.RegistrationInput) (handlers.TokenPairView, error)
	SignupEnabled(ctx context.Context) (bool, error)
	Signup(ctx context.Context, in handlers.RegistrationInput) (handlers.TokenPairView, error)
}

// DeviceLoginService is the slice of *handlers.AuthHandler the device-pairing
// operations use.
type DeviceLoginService interface {
	DeviceLoginConfigured() bool
	StartDeviceLogin(ctx context.Context, input auth.DeviceLoginStartInput) (*auth.DeviceLoginStartResult, error)
	LookupDeviceLogin(ctx context.Context, input auth.DeviceLoginLookupInput) (*auth.DeviceLoginInfo, error)
	PollDeviceLogin(ctx context.Context, deviceCode string) (*handlers.DeviceLoginPollView, error)
	ApproveDeviceLogin(ctx context.Context, input auth.DeviceLoginLookupInput, userID int) (handlers.DeviceLoginDecision, error)
	ApproveDeviceHandoff(ctx context.Context, input auth.DeviceLoginLookupInput, userID int, profileID string) (handlers.DeviceLoginDecision, error)
	DenyDeviceLogin(ctx context.Context, input auth.DeviceLoginLookupInput, userID int) (handlers.DeviceLoginDecision, error)
}

// Record only after the actual adapter has mounted the handler. The observer
// receives detached values, never the adapter, handler or registration surface.
type routeSnapshotAdapter struct {
	huma.Adapter
	routes *[]streamtelemetry.WalkedRoute
}

func (a *routeSnapshotAdapter) Handle(op *huma.Operation, handler func(huma.Context)) {
	a.Adapter.Handle(op, handler)
	*a.routes = append(*a.routes, streamtelemetry.WalkedRoute{Method: op.Method, Pattern: op.Path})
}
