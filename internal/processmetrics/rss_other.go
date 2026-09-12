//go:build !linux && !darwin

package processmetrics

import "os"

func maximumRSS(*os.ProcessState) (float64, bool) { return 0, false }
