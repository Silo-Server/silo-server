package handlers

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
)

type accessGroupHandlerTestStore struct {
	nextID int64
	groups map[int64]access.Group
}

func newAccessGroupHandlerTestStore() *accessGroupHandlerTestStore {
	return &accessGroupHandlerTestStore{
		nextID: 1,
		groups: map[int64]access.Group{},
	}
}

func (s *accessGroupHandlerTestStore) List(context.Context) ([]access.Group, error) {
	groups := make([]access.Group, 0, len(s.groups))
	for _, group := range s.groups {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].ID < groups[j].ID
	})
	return groups, nil
}

func (s *accessGroupHandlerTestStore) Get(_ context.Context, id int64) (*access.Group, error) {
	group, ok := s.groups[id]
	if !ok {
		return nil, access.ErrGroupNotFound
	}
	return &group, nil
}

func (s *accessGroupHandlerTestStore) Create(_ context.Context, input access.CreateGroupInput) (*access.Group, error) {
	now := time.Unix(1, 0).UTC()
	group := access.Group{
		ID:                       s.nextID,
		Name:                     input.Name,
		Description:              input.Description,
		LibraryIDs:               slices.Clone(input.LibraryIDs),
		MaxPlaybackQuality:       input.MaxPlaybackQuality,
		DownloadAllowed:          input.DownloadAllowed,
		DownloadTranscodeAllowed: input.DownloadTranscodeAllowed,
		MaxStreams:               input.MaxStreams,
		MaxTranscodes:            input.MaxTranscodes,
		AllowedPermissions:       slices.Clone(input.AllowedPermissions),
		RequestsAllowed:          input.RequestsAllowed,
		IsDefault:                input.IsDefault,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if input.IsDefault {
		s.clearDefault()
	}
	s.groups[group.ID] = group
	s.nextID++
	return &group, nil
}

func (s *accessGroupHandlerTestStore) Update(_ context.Context, id int64, input access.UpdateGroupInput) (*access.Group, error) {
	group, ok := s.groups[id]
	if !ok {
		return nil, access.ErrGroupNotFound
	}
	if input.Name != nil {
		group.Name = *input.Name
	}
	if input.Description != nil {
		group.Description = *input.Description
	}
	if input.LibraryIDs != nil {
		group.LibraryIDs = slices.Clone(*input.LibraryIDs)
	}
	if input.MaxPlaybackQuality != nil {
		group.MaxPlaybackQuality = *input.MaxPlaybackQuality
	}
	if input.DownloadAllowed != nil {
		group.DownloadAllowed = *input.DownloadAllowed
	}
	if input.DownloadTranscodeAllowed != nil {
		group.DownloadTranscodeAllowed = *input.DownloadTranscodeAllowed
	}
	if input.MaxStreams != nil {
		group.MaxStreams = *input.MaxStreams
	}
	if input.MaxTranscodes != nil {
		group.MaxTranscodes = *input.MaxTranscodes
	}
	if input.AllowedPermissions != nil {
		group.AllowedPermissions = slices.Clone(*input.AllowedPermissions)
	}
	if input.RequestsAllowed != nil {
		group.RequestsAllowed = *input.RequestsAllowed
	}
	if input.IsDefault != nil {
		if *input.IsDefault {
			s.clearDefault()
		} else if group.IsDefault {
			return nil, access.ErrDefaultGroupRequired
		}
		group.IsDefault = *input.IsDefault
	}
	group.UpdatedAt = time.Unix(2, 0).UTC()
	s.groups[id] = group
	return &group, nil
}

func (s *accessGroupHandlerTestStore) Delete(_ context.Context, id int64) error {
	group, ok := s.groups[id]
	if !ok {
		return access.ErrGroupNotFound
	}
	if group.IsDefault {
		return access.ErrDefaultGroupRequired
	}
	delete(s.groups, id)
	return nil
}

func (s *accessGroupHandlerTestStore) clearDefault() {
	for id, group := range s.groups {
		group.IsDefault = false
		s.groups[id] = group
	}
}
