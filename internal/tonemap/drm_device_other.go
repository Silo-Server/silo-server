//go:build !linux

package tonemap

// charDeviceNumber reports no device number: DRM render nodes exist only on
// Linux.
func charDeviceNumber(string) (string, bool) { return "", false }
