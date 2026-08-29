//go:build linux

package jellycompat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processToken(pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	stat := string(data)
	commEnd := strings.LastIndex(stat, ") ")
	if commEnd == -1 || commEnd+2 >= len(stat) {
		return ""
	}
	fields := strings.Fields(stat[commEnd+2:])
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
