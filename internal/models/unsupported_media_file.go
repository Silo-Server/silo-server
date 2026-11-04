package models

import "time"

// UnsupportedMediaFileGroup is the files of one directory that a scan found
// but does not catalog, for one reason.
type UnsupportedMediaFileGroup struct {
	MediaFolderID int
	DirectoryPath string
	Reason        string
	FileCount     int
	// FileNames holds the files' names in name order, capped; FileCount
	// counts them all.
	FileNames   []string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}
