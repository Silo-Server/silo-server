package handlers

import (
	"testing"
	"time"

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

	tests := []struct {
		name   string
		person models.Person
		want   bool
	}{
		{
			name: "incomplete person with provider id",
			person: models.Person{
				ID:        1,
				Name:      "Incomplete",
				TmdbID:    "1",
				UpdatedAt: now,
			},
			want: true,
		},
		{
			name: "fresh complete person",
			person: models.Person{
				ID:        2,
				Name:      "Fresh",
				Bio:       "Bio",
				PhotoPath: "photo.jpg",
				BirthDate: &birthDate,
				TmdbID:    "2",
				UpdatedAt: now,
			},
			want: false,
		},
		{
			name: "stale complete person",
			person: models.Person{
				ID:        3,
				Name:      "Stale",
				Bio:       "Bio",
				PhotoPath: "photo.jpg",
				BirthDate: &birthDate,
				TmdbID:    "3",
				UpdatedAt: now.Add(-personMetadataStaleAfter - time.Hour),
			},
			want: true,
		},
		{
			name: "incomplete person without provider id",
			person: models.Person{
				ID:        4,
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
