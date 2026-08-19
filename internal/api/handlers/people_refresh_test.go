package handlers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type recordingPersonRefreshQueue struct {
	ids []int64
}

func (q *recordingPersonRefreshQueue) Enqueue(id int64) {
	q.ids = append(q.ids, id)
}

func TestEnqueuePersonRefreshIfDue(t *testing.T) {
	now := time.Now()
	birthDate := now.AddDate(-30, 0, 0)
	justAttempted := now.Add(-30 * time.Second)
	attemptedLongAgo := now.Add(-catalog.PersonRefreshRetryAfter - time.Hour)

	tests := []struct {
		name   string
		person models.Person
		want   bool
	}{
		{
			name: "incomplete person never attempted",
			person: models.Person{
				ID:        1,
				Name:      "Incomplete",
				TmdbID:    "1",
				UpdatedAt: now,
			},
			want: true,
		},
		{
			name: "incomplete person attempted moments ago",
			person: models.Person{
				ID:                         2,
				Name:                       "Just Attempted",
				TmdbID:                     "2",
				UpdatedAt:                  now,
				MetadataRefreshAttemptedAt: &justAttempted,
			},
			want: false,
		},
		{
			name: "incomplete person past the retry window",
			person: models.Person{
				ID:                         3,
				Name:                       "Retry Due",
				TmdbID:                     "3",
				UpdatedAt:                  now,
				MetadataRefreshAttemptedAt: &attemptedLongAgo,
			},
			want: true,
		},
		{
			name: "fresh complete person",
			person: models.Person{
				ID:        4,
				Name:      "Fresh",
				Bio:       "Bio",
				PhotoPath: "photo.jpg",
				BirthDate: &birthDate,
				TmdbID:    "4",
				UpdatedAt: now,
			},
			want: false,
		},
		{
			name: "stale complete person",
			person: models.Person{
				ID:        5,
				Name:      "Stale",
				Bio:       "Bio",
				PhotoPath: "photo.jpg",
				BirthDate: &birthDate,
				TmdbID:    "5",
				UpdatedAt: now.Add(-catalog.PersonMetadataStaleAfter - time.Hour),
			},
			want: true,
		},
		{
			name: "stale complete person attempted moments ago",
			person: models.Person{
				ID:                         6,
				Name:                       "Stale But Attempted",
				Bio:                        "Bio",
				PhotoPath:                  "photo.jpg",
				BirthDate:                  &birthDate,
				TmdbID:                     "6",
				UpdatedAt:                  now.Add(-catalog.PersonMetadataStaleAfter - time.Hour),
				MetadataRefreshAttemptedAt: &justAttempted,
			},
			want: false,
		},
		{
			name: "person whose provider has no photo",
			person: models.Person{
				ID:        7,
				Name:      "No Photo Available",
				Bio:       "Bio",
				PhotoPath: "-",
				BirthDate: &birthDate,
				TmdbID:    "7",
				UpdatedAt: now,
			},
			want: false,
		},
		{
			name: "incomplete person without provider id",
			person: models.Person{
				ID:        8,
				Name:      "Local",
				UpdatedAt: now,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := &recordingPersonRefreshQueue{}
			handler := &PeopleHandler{refreshQueue: queue}

			handler.enqueuePersonRefreshIfDue(tt.person)

			got := len(queue.ids) == 1
			if got != tt.want {
				t.Fatalf("queued = %v, want %v", got, tt.want)
			}
		})
	}
}
