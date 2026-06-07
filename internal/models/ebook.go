package models

import "time"

type EbookDetails struct {
	ContentID    string
	Format       string
	ISBN         string
	ASIN         string
	Publisher    string
	PageCount    int
	SeriesName   string
	SeriesIndex  string
	MetadataJSON []byte
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
