// Package capability exposes stable Silo plugin capability type strings.
package capability

const (
	EventConsumer    = "event_consumer.v1"
	HTTPRoutes       = "http_routes.v1"
	ScheduledTask    = "scheduled_task.v1"
	RequestRouter    = "request_router.v1"
	MediaAnalyzer    = "media_analyzer.v1"
	AuthProvider     = "auth_provider.v1"
	MetadataProvider = "metadata_provider.v1"
	MarkerProvider   = "marker_provider.v1"
	AudiobookBackend = "audiobook_backend.v1"
	EbookBackend     = "ebook_backend.v1"
	ScanSource       = "scan_source.v1"
)

// KnownTypes lists every capability type recognized by this SDK version.
var KnownTypes = []string{
	EventConsumer,
	HTTPRoutes,
	ScheduledTask,
	RequestRouter,
	MediaAnalyzer,
	AuthProvider,
	MetadataProvider,
	MarkerProvider,
	AudiobookBackend,
	EbookBackend,
	ScanSource,
}
