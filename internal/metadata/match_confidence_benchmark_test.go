package metadata

import (
	"fmt"
	"testing"
)

// These fixtures measure title selection CPU work, without provider or I/O
// latency. Large result and alias counts exercise the upper end of the work.
func BenchmarkSelectBestMatchYear(b *testing.B) {
	for _, fixture := range []struct {
		name, want string
		titles     []string
		aliases    []string
	}{
		{
			name: "english", want: "Mother of Storms",
			titles:  []string{"Mother of Storms (Unabridged)", "Mother of Storms: A Novel", "The Storm Mother", "A Gathering Storm", "Looking for a Miracle", "Storm Front"},
			aliases: []string{"Mother of Storms", "The Mother of Storms", "Storm Mother", "La mère des tempêtes", "嵐の母", "Mutter der Stürme", "Mother of Storms: Special Edition", "Mother of Storms: A Novel", "Mother of Storms (Audiobook)", "Storms"},
		},
		{
			name: "volume", want: "Op-Center 4 - Acts of War",
			titles:  []string{"Tom Clancy's Op-Center #4: Acts of War", "Acts of War", "Op-Center 4", "Tom Clancy's Op-Center, Book 3", "Op-Center: Mirror Image", "Acts of War (Unabridged)"},
			aliases: []string{"Op-Center", "Acts of War", "Op-Center Book IV: Acts of War", "Actes de guerre", "Krigshandlinger", "Acts of War (Unabridged)", "Op-Center Vol. 4", "Tom Clancy's Acts of War", "Acts of War: Special Edition", "Op-Center, Book 4"},
		},
		{
			name: "non-latin", want: "進撃の巨人",
			titles:  []string{"Attack on Titan", "進撃の巨人", "Shingeki no Kyojin", "進撃！巨人中学校", "進撃の巨人 Before the Fall", "進撃の巨人 (Light Novel)"},
			aliases: []string{"進撃の巨人", "Attack on Titan", "Shingeki no Kyojin", "L’Attaque des Titans", "Атака титанов", "Ataque a los Titanes", "進撃の巨人 完全版", "Attack on Titan: Special Edition", "진격의 거인", "進擊的巨人"},
		},
	} {
		for _, count := range []int{1, 20, 100} {
			for _, aliasCount := range []int{0, 3, 10} {
				b.Run(fmt.Sprintf("%s/candidates=%d/aliases=%d", fixture.name, count, aliasCount), func(b *testing.B) {
					results := make([]SearchResult, count)
					for i := range results {
						results[i] = SearchResult{Name: fixture.titles[i%len(fixture.titles)], Year: 1994 + i%5}
						for alias := range aliasCount {
							results[i].TitleAliases = append(results[i].TitleAliases, TitleAlias{Title: fixture.aliases[(i+alias)%len(fixture.aliases)]})
						}
					}
					b.ReportAllocs()
					for b.Loop() {
						selectBestMatchYear(fixture.want, 1996, results)
					}
				})
			}
		}
	}
}
