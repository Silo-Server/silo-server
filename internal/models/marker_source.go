package models

const (
	MarkerSourceScanner = "scanner"
	MarkerSourceS3      = "s3"
	MarkerSourceOnline  = "online"
	MarkerSourcePlugin  = "plugin"
	MarkerSourceManual  = "manual"
)

// MarkerSourcePriority returns a higher value for stronger marker sources.
func MarkerSourcePriority(source string) int {
	switch source {
	case MarkerSourceManual:
		return 4
	case MarkerSourcePlugin, MarkerSourceOnline:
		return 3
	case MarkerSourceS3:
		return 2
	case MarkerSourceScanner:
		return 1
	default:
		return 0
	}
}
