package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCapServerCollections(t *testing.T) {
	makeN := func(n int) []*models.LibraryCollection {
		out := make([]*models.LibraryCollection, n)
		for i := range out {
			out[i] = &models.LibraryCollection{ID: string(rune('a' + i%26))}
		}
		return out
	}

	t.Run("under cap returns all, total equals length", func(t *testing.T) {
		in := makeN(serverCollectionsPerLibraryCap - 3)
		got, total := capServerCollections(in)
		if total != serverCollectionsPerLibraryCap-3 {
			t.Fatalf("total = %d, want %d", total, serverCollectionsPerLibraryCap-3)
		}
		if len(got) != len(in) {
			t.Fatalf("len(got) = %d, want %d", len(got), len(in))
		}
	})

	t.Run("at cap returns all", func(t *testing.T) {
		in := makeN(serverCollectionsPerLibraryCap)
		got, total := capServerCollections(in)
		if total != serverCollectionsPerLibraryCap || len(got) != serverCollectionsPerLibraryCap {
			t.Fatalf("got len=%d total=%d, want both %d", len(got), total, serverCollectionsPerLibraryCap)
		}
	})

	t.Run("over cap trims slice but reports full total", func(t *testing.T) {
		in := makeN(serverCollectionsPerLibraryCap + 17)
		got, total := capServerCollections(in)
		if len(got) != serverCollectionsPerLibraryCap {
			t.Fatalf("len(got) = %d, want %d", len(got), serverCollectionsPerLibraryCap)
		}
		if total != serverCollectionsPerLibraryCap+17 {
			t.Fatalf("total = %d, want %d", total, serverCollectionsPerLibraryCap+17)
		}
	})
}
