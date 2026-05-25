package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestTrustedIDValue_UsesMetadbContentID(t *testing.T) {
	t.Parallel()

	hints := &MatchHints{ContentID: "meta-123"}
	if got := trustedIDValue(hints, "metadb"); got != "meta-123" {
		t.Fatalf("trustedIDValue(metadb) = %q, want meta-123", got)
	}
}

func TestNormalizeCandidates(t *testing.T) {
	tests := []struct {
		name    string
		results []SearchResult
		content string
		wantLen int
		check   func(t *testing.T, candidates []MatchCandidate)
	}{
		{
			name: "merge two providers with identical provider ID fingerprint",
			results: []SearchResult{
				{
					Name:        "The Matrix",
					Year:        1999,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "603"},
					ImageURL:    "https://tmdb.org/matrix.jpg",
					Overview:    "A computer hacker learns about the true nature of reality.",
				},
				{
					Name:        "The Matrix",
					Year:        1999,
					Provider:    "metadb",
					ProviderIDs: map[string]string{"tmdb": "603"},
					Overview:    "Neo discovers the Matrix.",
				},
			},
			content: "movie",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				c := candidates[0]
				if c.Title != "The Matrix" {
					t.Errorf("Title = %q, want %q", c.Title, "The Matrix")
				}
				if c.ProviderIDs["tmdb"] != "603" {
					t.Errorf("ProviderIDs[tmdb] = %q, want %q", c.ProviderIDs["tmdb"], "603")
				}
				if len(c.Sources) != 2 {
					t.Fatalf("Sources len = %d, want 2", len(c.Sources))
				}
				// Sources are sorted alphabetically.
				if c.Sources[0] != "metadb" || c.Sources[1] != "tmdb" {
					t.Errorf("Sources = %v, want [metadb tmdb]", c.Sources)
				}
			},
		},
		{
			name: "merge compatible candidates with overlapping provider IDs",
			results: []SearchResult{
				{
					Name:        "The Rookie: Feds",
					Year:        2022,
					Provider:    "tvdb",
					ProviderIDs: map[string]string{"tvdb": "420105", "imdb": "tt18076310"},
				},
				{
					Name:        "The Rookie: Feds",
					Year:        2022,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "201992", "tvdb": "420105", "imdb": "tt18076310"},
				},
			},
			content: "series",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				c := candidates[0]
				if c.ProviderIDs["tmdb"] != "201992" {
					t.Fatalf("tmdb id = %q, want 201992", c.ProviderIDs["tmdb"])
				}
				if c.ProviderIDs["tvdb"] != "420105" || c.ProviderIDs["imdb"] != "tt18076310" {
					t.Fatalf("provider ids = %+v, want tvdb and imdb preserved", c.ProviderIDs)
				}
				if len(c.Sources) != 2 {
					t.Fatalf("sources = %+v, want two providers", c.Sources)
				}
			},
		},
		{
			name: "do not merge candidates with conflicting overlapping provider IDs",
			results: []SearchResult{
				{
					Name:        "Show A",
					Year:        2022,
					Provider:    "tvdb",
					ProviderIDs: map[string]string{"tvdb": "420105", "imdb": "tt18076310"},
				},
				{
					Name:        "Show B",
					Year:        2022,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "201992", "tvdb": "999999", "imdb": "tt18076310"},
				},
			},
			content: "series",
			wantLen: 2,
			check: func(t *testing.T, candidates []MatchCandidate) {
				if len(candidates) != 2 {
					t.Fatalf("len(candidates) = %d, want 2", len(candidates))
				}
			},
		},
		{
			name: "no recognized provider IDs gets synthetic key and stays separate",
			results: []SearchResult{
				{
					Name:        "Obscure Film",
					Year:        2020,
					Provider:    "custom-provider",
					ProviderIDs: map[string]string{"custom": "abc123"},
				},
				{
					Name:        "Another Film",
					Year:        2021,
					Provider:    "custom-provider",
					ProviderIDs: map[string]string{"custom": "def456"},
				},
			},
			content: "movie",
			wantLen: 2,
			check: func(t *testing.T, candidates []MatchCandidate) {
				if candidates[0].Title != "Obscure Film" {
					t.Errorf("candidates[0].Title = %q, want %q", candidates[0].Title, "Obscure Film")
				}
				if candidates[1].Title != "Another Film" {
					t.Errorf("candidates[1].Title = %q, want %q", candidates[1].Title, "Another Film")
				}
				// Each should have exactly one source.
				for i, c := range candidates {
					if len(c.Sources) != 1 {
						t.Errorf("candidates[%d].Sources len = %d, want 1", i, len(c.Sources))
					}
				}
			},
		},
		{
			name: "agreement hints computed when 2+ sources agree",
			results: []SearchResult{
				{
					Name:        "Inception",
					Year:        2010,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "27205"},
				},
				{
					Name:        "Inception",
					Year:        2010,
					Provider:    "tvdb",
					ProviderIDs: map[string]string{"tmdb": "27205"},
				},
			},
			content: "movie",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				c := candidates[0]
				if len(c.AgreementHints) != 1 {
					t.Fatalf("AgreementHints len = %d, want 1", len(c.AgreementHints))
				}
				want := "agreed_by_tmdb_and_tvdb"
				if c.AgreementHints[0] != want {
					t.Errorf("AgreementHints[0] = %q, want %q", c.AgreementHints[0], want)
				}
			},
		},
		{
			name: "no agreement hint for single source",
			results: []SearchResult{
				{
					Name:        "Solo",
					Year:        2023,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "99999"},
				},
			},
			content: "movie",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				if len(candidates[0].AgreementHints) != 0 {
					t.Errorf("AgreementHints = %v, want empty", candidates[0].AgreementHints)
				}
			},
		},
		{
			name: "ImageURL and Overview fallback to first non-empty",
			results: []SearchResult{
				{
					Name:        "Dune",
					Year:        2021,
					Provider:    "provider-a",
					ProviderIDs: map[string]string{"tmdb": "438631"},
					ImageURL:    "",
					Overview:    "",
				},
				{
					Name:        "Dune",
					Year:        2021,
					Provider:    "provider-b",
					ProviderIDs: map[string]string{"tmdb": "438631"},
					ImageURL:    "https://example.com/dune.jpg",
					Overview:    "A noble family becomes embroiled in a war.",
				},
				{
					Name:        "Dune",
					Year:        2021,
					Provider:    "provider-c",
					ProviderIDs: map[string]string{"tmdb": "438631"},
					ImageURL:    "https://other.com/dune2.jpg",
					Overview:    "Should not win; provider-b was first.",
				},
			},
			content: "movie",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				c := candidates[0]
				if c.ImageURL != "https://example.com/dune.jpg" {
					t.Errorf("ImageURL = %q, want first non-empty from provider-b", c.ImageURL)
				}
				if c.Overview != "A noble family becomes embroiled in a war." {
					t.Errorf("Overview = %q, want first non-empty from provider-b", c.Overview)
				}
			},
		},
		{
			name: "insertion order stability",
			results: []SearchResult{
				{
					Name:        "First",
					Year:        2001,
					Provider:    "p1",
					ProviderIDs: map[string]string{"tmdb": "1"},
				},
				{
					Name:        "Second",
					Year:        2002,
					Provider:    "p2",
					ProviderIDs: map[string]string{"tmdb": "2"},
				},
				{
					Name:        "Third",
					Year:        2003,
					Provider:    "p3",
					ProviderIDs: map[string]string{"tmdb": "3"},
				},
			},
			content: "movie",
			wantLen: 3,
			check: func(t *testing.T, candidates []MatchCandidate) {
				titles := []string{"First", "Second", "Third"}
				for i, want := range titles {
					if candidates[i].Title != want {
						t.Errorf("candidates[%d].Title = %q, want %q", i, candidates[i].Title, want)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeCandidates(tt.results, tt.content)
			if len(got) != tt.wantLen {
				t.Fatalf("len(candidates) = %d, want %d", len(got), tt.wantLen)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestSelectInitialMatchCandidate_AcceptsSinglePunctuationEquivalentCandidate(t *testing.T) {
	tests := []struct {
		name           string
		hintTitle      string
		candidateTitle string
		year           int
	}{
		{
			name:           "colon variant",
			hintTitle:      "Anchorman The Legend of Ron Burgundy",
			candidateTitle: "Anchorman: The Legend of Ron Burgundy",
			year:           2004,
		},
		{
			name:           "apostrophe and question mark variant",
			hintTitle:      "Whats Your Number",
			candidateTitle: "What's Your Number?",
			year:           2011,
		},
		{
			name:           "ampersand variant",
			hintTitle:      "Tromeo and Juliet",
			candidateTitle: "Tromeo & Juliet",
			year:           1996,
		},
		{
			name:           "hyphen variant",
			hintTitle:      "Ant Man and the Wasp",
			candidateTitle: "Ant-Man and the Wasp",
			year:           2018,
		},
		{
			name:           "superscript digit variant",
			hintTitle:      "Alien 3",
			candidateTitle: "Alien³",
			year:           1992,
		},
		{
			name:           "comparison safe edition suffix variant",
			hintTitle:      "Zack Snyders Justice League Justice Is Gray",
			candidateTitle: "Zack Snyder's Justice League",
			year:           2021,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			winner, ok := selectInitialMatchCandidate(
				&MatchHints{
					Title: tt.hintTitle,
					Year:  tt.year,
					Type:  "movie",
				},
				[]MatchCandidate{
					{
						Title:       tt.candidateTitle,
						Year:        tt.year,
						ContentType: "movie",
						ProviderIDs: map[string]string{"tmdb": "123"},
						Sources:     []string{"tmdb"},
					},
				},
			)
			if !ok || winner == nil {
				t.Fatalf("expected lone punctuation-equivalent candidate to be accepted")
			}
			if winner.Title != tt.candidateTitle {
				t.Fatalf("winner.Title = %q, want %q", winner.Title, tt.candidateTitle)
			}
		})
	}
}

func TestSelectInitialMatchCandidate_RejectsWeakSingleCandidate(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "Anchorman The Legend of Ron Burgundy",
			Year:  2004,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "Step Brothers",
				Year:        2008,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "12133"},
				Sources:     []string{"tmdb"},
			},
		},
	)
	if ok || winner != nil {
		t.Fatalf("expected weak lone candidate to be rejected")
	}
}

func TestSelectRefreshMatchCandidate_AcceptsCandidateWithPartialTrustedIDCoverage(t *testing.T) {
	winner, ok := selectRefreshMatchCandidate(
		&models.MediaItem{
			Title:  "The Matrix",
			Year:   1999,
			Type:   "movie",
			TmdbID: "603",
			ImdbID: "tt0133093",
		},
		[]MatchCandidate{
			{
				Title:       "The Matrix",
				Year:        1999,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "603"},
				Sources:     []string{"tmdb"},
			},
		},
	)
	if !ok || winner == nil {
		t.Fatalf("expected partial trusted-ID coverage candidate to be accepted")
	}
}

func TestSelectRefreshMatchCandidate_RejectsCandidateWithoutTrustedIDMatches(t *testing.T) {
	winner, ok := selectRefreshMatchCandidate(
		&models.MediaItem{
			Title:  "The Matrix",
			Year:   1999,
			Type:   "movie",
			TmdbID: "603",
			ImdbID: "tt0133093",
		},
		[]MatchCandidate{
			{
				Title:       "The Matrix",
				Year:        1999,
				ContentType: "movie",
				ProviderIDs: map[string]string{},
				Sources:     []string{"tmdb"},
			},
		},
	)
	if ok || winner != nil {
		t.Fatalf("expected candidate without trusted-ID matches to be rejected")
	}
}

func TestSelectRefreshMatchCandidate_RejectsConflictingTrustedIDCandidate(t *testing.T) {
	winner, ok := selectRefreshMatchCandidate(
		&models.MediaItem{
			Title:  "The Matrix",
			Year:   1999,
			Type:   "movie",
			TmdbID: "603",
			ImdbID: "tt0133093",
		},
		[]MatchCandidate{
			{
				Title:       "The Matrix",
				Year:        1999,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "603", "imdb": "tt9999999"},
				Sources:     []string{"tmdb"},
			},
		},
	)
	if ok || winner != nil {
		t.Fatalf("expected conflicting trusted-ID candidate to be rejected")
	}
}
