// Command backfill-library-matching migrates skipped roots from the legacy
// scanning model into the canonical-root / root-claim system. For each skipped
// root it re-derives the canonical root using the current naming rules, groups
// skipped roots by canonical root, and either creates a new media item or
// reuses an existing one. Files under each root group are bulk-linked to the
// owning content item.
//
// Usage:
//
//	go run ./scripts/backfill-library-matching -library-id=1 -dry-run=true
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

var (
	libraryID = flag.Int("library-id", 0, "limit backfill to one library (media_folder_id)")
	dryRun    = flag.Bool("dry-run", true, "report planned changes without writing")
	force     = flag.Bool("force-while-scans-running", false, "allow running even if scans may be active (dangerous)")
)

func main() {
	flag.Parse()

	if *libraryID == 0 {
		slog.Error("backfill: -library-id is required")
		os.Exit(1)
	}

	if !*dryRun && !*force {
		slog.Error("backfill: refusing to run in write mode without -force-while-scans-running; "+
			"pause scans for the target library first, then re-run with -force-while-scans-running=true",
			"library_id", *libraryID)
		os.Exit(1)
	}

	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("backfill: DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("backfill: failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("backfill: failed to ping database", "error", err)
		os.Exit(1)
	}
	slog.Info("backfill: connected to database")

	skippedRepo := metadata.NewSkippedRootRepository(pool)
	rootClaimRepo := catalog.NewRootClaimRepository(pool)
	itemRepo := catalog.NewItemRepository(pool)
	folderRepo := catalog.NewFolderRepository(pool)

	folderID := *libraryID
	folder, err := folderRepo.GetByID(ctx, folderID)
	if err != nil {
		slog.Error("backfill: failed to load library", "error", err, "folder_id", folderID)
		os.Exit(1)
	}
	libraryType := folder.Type

	// Load skipped roots for the target folder.
	skipped, err := skippedRepo.ListByFolder(ctx, folderID)
	if err != nil {
		slog.Error("backfill: failed to list skipped roots", "error", err, "folder_id", folderID)
		os.Exit(1)
	}

	if len(skipped) == 0 {
		slog.Info("backfill: no skipped roots found", "folder_id", folderID)
		return
	}

	slog.Info("backfill: loaded skipped roots", "folder_id", folderID, "count", len(skipped))

	// Group by canonical root.
	groups := GroupByCanonicalRoot(skipped, libraryType)
	slog.Info("backfill: grouped into canonical roots", "group_count", len(groups))

	var (
		createCount int
		reuseCount  int
		fileCount   int
	)

	for _, group := range groups {
		log := slog.With(
			"canonical_root", group.CanonicalRoot,
			"type", group.Type,
			"skipped_roots", len(group.SkippedRoots),
		)

		// Check if there is already a root claim for this canonical root.
		existing, err := rootClaimRepo.Get(ctx, folderID, group.CanonicalRoot)
		if err != nil {
			log.Error("backfill: failed to check existing root claim", "error", err)
			continue
		}

		var contentID string
		if existing != nil {
			contentID = existing.ContentID
			reuseCount++
			log.Info("backfill: reusing existing item", "content_id", contentID)
		} else {
			// Derive title from the canonical root folder name.
			folderName := filepath.Base(group.CanonicalRoot)
			fnHints := naming.ParseFilename(folderName, group.Type)

			title := folderName
			year := 0
			if fnHints != nil && fnHints.Title != "" {
				title = fnHints.Title
				year = fnHints.Year
			}

			if *dryRun {
				contentID = fmt.Sprintf("dry-run-%d", createCount+1)
				createCount++
				log.Info("backfill: [DRY-RUN] would create item",
					"title", title,
					"year", year,
					"content_id", contentID,
				)
			} else {
				newID, err := idgen.NextID()
				if err != nil {
					log.Error("backfill: failed to generate content_id", "error", err)
					continue
				}
				contentID = newID

				item := &models.MediaItem{
					ContentID: contentID,
					Type:      group.Type,
					Title:     title,
					SortTitle: title,
					Year:      year,
					Status:    "pending",
				}

				if err := itemRepo.Upsert(ctx, item); err != nil {
					log.Error("backfill: failed to create item", "error", err)
					continue
				}
				createCount++
				log.Info("backfill: created item",
					"content_id", contentID,
					"title", title,
					"year", year,
				)
			}
		}

		if *dryRun {
			// Count how many skipped root files would be linked.
			totalFiles := 0
			for _, sr := range group.SkippedRoots {
				totalFiles += sr.FileCount
			}
			fileCount += totalFiles
			log.Info("backfill: [DRY-RUN] would claim root and relink files",
				"content_id", contentID,
				"estimated_files", totalFiles,
			)
		} else {
			linked, err := rootClaimRepo.ClaimAndRelinkFiles(ctx, folderID, group.CanonicalRoot, contentID)
			if err != nil {
				log.Error("backfill: failed to claim and relink", "error", err)
				continue
			}
			fileCount += linked
			log.Info("backfill: claimed root and relinked files",
				"content_id", contentID,
				"files_linked", linked,
			)
		}
	}

	mode := "DRY-RUN"
	if !*dryRun {
		mode = "WRITE"
	}

	slog.Info("backfill: complete",
		"mode", mode,
		"folder_id", folderID,
		"groups", len(groups),
		"items_created", createCount,
		"items_reused", reuseCount,
		"files_linked", fileCount,
	)
}

// CanonicalRootGroup represents a set of skipped roots that share the same
// canonical root path. All files under these skipped roots belong to one item.
type CanonicalRootGroup struct {
	CanonicalRoot string
	Type          string // "movie" or "series"
	SkippedRoots  []*models.SkippedMediaRoot
}

// GroupByCanonicalRoot re-derives the canonical root for each skipped root
// using the current naming rules and groups them. Skipped roots whose sample
// file path cannot be resolved to a canonical root are placed under the
// skipped root path itself as a fallback.
func GroupByCanonicalRoot(skipped []*models.SkippedMediaRoot, libraryType string) []CanonicalRootGroup {
	type groupKey struct {
		root     string
		rootType string
	}

	groupMap := make(map[groupKey]*CanonicalRootGroup)
	var order []groupKey

	for _, sr := range skipped {
		var canonRoot string
		var rootType string

		// Use the sample file path if available for more accurate canonical
		// root detection. Fall back to the skipped root path itself.
		probe := sr.SampleFilePath
		if probe == "" {
			probe = sr.RootPath + "/sample.mkv"
		}

		if cr, ok := naming.DetectCanonicalRoot(probe, libraryType); ok {
			canonRoot = cr.RootPath
			rootType = cr.Type
		} else {
			canonRoot = sr.RootPath
			rootType = "movie" // default fallback
		}

		key := groupKey{root: canonRoot, rootType: rootType}
		if _, exists := groupMap[key]; !exists {
			groupMap[key] = &CanonicalRootGroup{
				CanonicalRoot: canonRoot,
				Type:          rootType,
			}
			order = append(order, key)
		}
		groupMap[key].SkippedRoots = append(groupMap[key].SkippedRoots, sr)
	}

	result := make([]CanonicalRootGroup, 0, len(order))
	for _, key := range order {
		result = append(result, *groupMap[key])
	}
	return result
}
