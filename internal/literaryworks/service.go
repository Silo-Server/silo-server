package literaryworks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*DetailResponse, error) {
	if s == nil || s.repo == nil {
		return nil, ErrWorkNotFound
	}
	work, items, err := s.repo.GetWorkWithItems(ctx, workID, filter)
	if err != nil {
		return nil, err
	}
	resp := &DetailResponse{
		WorkID:    work.WorkID,
		WorkTitle: work.CanonicalTitle,
		Metadata: WorkMetadata{
			Description: work.Description,
			Genres:      work.Genres,
			Publisher:   work.Publisher,
		},
	}
	if work.PublishedDate != nil {
		resp.Metadata.PublishedDate = work.PublishedDate.Format("2006-01-02")
	}
	for _, item := range items {
		resp.Formats = append(resp.Formats, FormatResponse{
			Type:           item.FormatType,
			ContentID:      item.ContentID,
			LibraryID:      item.LibraryID,
			AvailableFiles: filesToResponse(item.Files),
			Progress:       item.Progress,
		})
	}
	return resp, nil
}

func (s *Service) ListCandidates(ctx context.Context, contentID string, limit int) ([]Candidate, error) {
	if s == nil || s.repo == nil {
		return nil, ErrWorkNotFound
	}
	source, err := s.repo.GetMatchItem(ctx, contentID)
	if err != nil {
		return nil, err
	}
	targets, err := s.repo.ListMatchCandidates(ctx, source.MatchItem, limit*5)
	if err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, len(targets))
	for _, target := range targets {
		candidate := ScoreCandidate(source.MatchItem, target.MatchItem)
		candidate.TargetWorkID = target.WorkID
		if candidate.Score >= 0.75 {
			candidates = append(candidates, candidate)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].TargetContentID < candidates[j].TargetContentID
		}
		return candidates[i].Score > candidates[j].Score
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func (s *Service) LinkItems(ctx context.Context, workID string, contentIDs []string) (string, error) {
	if s == nil || s.repo == nil {
		return "", ErrWorkNotFound
	}
	contentIDs = compactContentIDs(contentIDs)
	if len(contentIDs) == 0 {
		return "", ErrWorkNotFound
	}
	items := make([]MatchItemWithWork, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		item, err := s.repo.GetMatchItem(ctx, contentID)
		if err != nil {
			return "", err
		}
		items = append(items, item)
	}
	if strings.TrimSpace(workID) == "" {
		existing, err := s.repo.GetFirstWorkIDForContentIDs(ctx, contentIDs)
		if err != nil {
			return "", err
		}
		workID = existing
	}
	if strings.TrimSpace(workID) == "" {
		workID = generatedWorkID(items[0].MatchItem)
		if _, err := s.repo.CreateWork(ctx, CreateWorkParams{
			WorkID:           workID,
			CanonicalTitle:   items[0].Title,
			SortTitle:        items[0].Title,
			NormalizedTitle:  normalizeKey(items[0].Title),
			PrimaryAuthorKey: personKey(items[0].Authors),
			Publisher:        items[0].Publisher,
		}); err != nil {
			return "", err
		}
	}
	linkItems := make([]LinkItemParams, 0, len(items))
	for _, item := range items {
		linkItems = append(linkItems, LinkItemParams{
			ContentID:  item.ContentID,
			FormatType: item.Type,
			LinkSource: LinkManual,
			Confidence: 1,
		})
	}
	if err := s.repo.LinkItems(ctx, workID, linkItems); err != nil {
		return "", err
	}
	return workID, nil
}

func (s *Service) AutoLinkContent(ctx context.Context, contentID string) (string, bool, error) {
	if s == nil || s.repo == nil {
		return "", false, ErrWorkNotFound
	}
	// Cheap guard first: an item already linked to a work needs no candidate
	// scan. Rescans re-visit every unchanged book, so skipping the expensive
	// GetMatchItem + ListMatchCandidates path here is what keeps Postgres idle
	// on steady-state rescans instead of rebuilding lateral aggregates per book.
	if workID, err := s.repo.GetFirstWorkIDForContentIDs(ctx, []string{contentID}); err != nil {
		return "", false, err
	} else if strings.TrimSpace(workID) != "" {
		return workID, false, nil
	}
	source, err := s.repo.GetMatchItem(ctx, contentID)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(source.WorkID) != "" {
		return source.WorkID, false, nil
	}
	targets, err := s.repo.ListMatchCandidates(ctx, source.MatchItem, 100)
	if err != nil {
		return "", false, err
	}
	var best Candidate
	var bestTarget MatchItemWithWork
	for _, target := range targets {
		candidate := ScoreCandidate(source.MatchItem, target.MatchItem)
		if candidate.Score > best.Score {
			best = candidate
			bestTarget = target
		}
	}
	if best.Score < AutoLinkThreshold || best.TargetContentID == "" {
		return "", false, nil
	}
	workID := strings.TrimSpace(bestTarget.WorkID)
	if workID == "" {
		workID = generatedWorkID(source.MatchItem)
		if _, err := s.repo.CreateWork(ctx, CreateWorkParams{
			WorkID:           workID,
			CanonicalTitle:   source.Title,
			SortTitle:        source.Title,
			NormalizedTitle:  normalizeKey(source.Title),
			PrimaryAuthorKey: personKey(source.Authors),
			Publisher:        source.Publisher,
		}); err != nil {
			return "", false, err
		}
	}
	items := []LinkItemParams{{
		ContentID:  source.ContentID,
		FormatType: source.Type,
		LinkSource: best.LinkSource,
		Confidence: best.Score,
	}}
	if bestTarget.WorkID == "" {
		items = append(items, LinkItemParams{
			ContentID:  bestTarget.ContentID,
			FormatType: bestTarget.Type,
			LinkSource: best.LinkSource,
			Confidence: best.Score,
		})
	}
	if err := s.repo.LinkItems(ctx, workID, items); err != nil {
		return "", false, err
	}
	return workID, true, nil
}

func (s *Service) UnlinkItem(ctx context.Context, workID, contentID string) error {
	if s == nil || s.repo == nil {
		return ErrWorkNotFound
	}
	return s.repo.UnlinkItem(ctx, workID, contentID)
}

func (s *Service) ConfirmMatch(ctx context.Context, sourceContentID, targetContentID string, userID int) (string, error) {
	if s == nil || s.repo == nil {
		return "", ErrWorkNotFound
	}
	workID, err := s.LinkItems(ctx, "", []string{sourceContentID, targetContentID})
	if err != nil {
		return "", err
	}
	if err := s.repo.RecordDecision(ctx, sourceContentID, targetContentID, DecisionConfirmed, userID); err != nil {
		return "", err
	}
	return workID, nil
}

func (s *Service) IgnoreMatch(ctx context.Context, sourceContentID, targetContentID string, userID int) error {
	if s == nil || s.repo == nil {
		return ErrWorkNotFound
	}
	return s.repo.RecordDecision(ctx, sourceContentID, targetContentID, DecisionIgnored, userID)
}

func filesToResponse(files []WorkFile) []FileResponse {
	out := make([]FileResponse, 0, len(files))
	for _, f := range files {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(f.FilePath)), ".")
		out = append(out, FileResponse{
			FileID:          f.FileID,
			OriginalName:    filepath.Base(f.FilePath),
			Format:          ext,
			MIMEType:        mime.TypeByExtension("." + ext),
			Size:            f.Size,
			DurationSeconds: f.DurationSeconds,
		})
	}
	return out
}

func compactContentIDs(contentIDs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		contentID = strings.TrimSpace(contentID)
		if contentID == "" {
			continue
		}
		if _, ok := seen[contentID]; ok {
			continue
		}
		seen[contentID] = struct{}{}
		out = append(out, contentID)
	}
	return out
}

func generatedWorkID(item MatchItem) string {
	title := normalizeKey(item.Title)
	author := personKey(item.Authors)
	sum := sha256.Sum256([]byte(title + "|" + author))
	return "work-" + hex.EncodeToString(sum[:])[:16]
}
