package artworkstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type conditionalS3Creator interface {
	PutObjectIfAbsent(ctx context.Context, bucket, key string, data []byte) (bool, error)
}

// ensureSentinels verifies the fixed format marker and returns the random UUID
// of this physical bucket copy. Both live at fixed private keys outside the
// logical artwork grammar and are never exposed through delivery routes.
func (s *S3Store) ensureSentinels(ctx context.Context, allowLegacyNonEmpty bool) (string, bool, error) {
	initialized := false
	format, err := s.readSentinel(ctx, formatMarkerFileName)
	switch {
	case err == nil:
		if string(format) != formatMarkerContents {
			return "", false, fmt.Errorf("%w: s3 artwork format marker is invalid", ErrStoreIdentity)
		}
	case errors.Is(err, ErrNotFound):
		if hasObjects, listErr := s.hasArtworkObjects(ctx); listErr != nil {
			return "", false, listErr
		} else if hasObjects && !allowLegacyNonEmpty {
			return "", false, fmt.Errorf("%w: s3 format marker is missing from a non-empty artwork store", ErrStoreIdentity)
		}
		if err := s.createSentinel(ctx, formatMarkerFileName, []byte(formatMarkerContents)); err != nil {
			return "", false, err
		}
		initialized = true
	default:
		return "", false, err
	}

	raw, err := s.readSentinel(ctx, markerFileName)
	if errors.Is(err, ErrNotFound) {
		if hasObjects, listErr := s.hasArtworkObjects(ctx); listErr != nil {
			return "", false, listErr
		} else if hasObjects && !allowLegacyNonEmpty {
			return "", false, fmt.Errorf("%w: s3 copy marker is missing from a non-empty artwork store", ErrStoreIdentity)
		}
		id, idErr := newMarkerID()
		if idErr != nil {
			return "", false, idErr
		}
		encoded, encodeErr := json.Marshal(Marker{
			Version: markerFormatVersion, ID: id,
			CreatedAt: time.Now().UTC().Truncate(time.Second),
		})
		if encodeErr != nil {
			return "", false, encodeErr
		}
		encoded = append(encoded, '\n')
		if err := s.createSentinel(ctx, markerFileName, encoded); err != nil {
			return "", false, err
		}
		initialized = true
		raw, err = s.readSentinel(ctx, markerFileName)
	}
	if err != nil {
		return "", false, err
	}
	var marker Marker
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil || marker.Version != markerFormatVersion || marker.ID == "" {
		return "", false, fmt.Errorf("%w: s3 copy marker is invalid", ErrStoreIdentity)
	}
	return marker.ID, initialized, nil
}

func (s *S3Store) hasArtworkObjects(ctx context.Context) (bool, error) {
	const pageSize = 256
	cursor := ""
	for {
		objects, next, done, err := s.client.ListObjectInfosPage(ctx, s.bucket, "", cursor, pageSize)
		if err != nil {
			return false, err
		}
		for _, object := range objects {
			if object.Key == formatMarkerFileName || object.Key == markerFileName {
				continue
			}
			if artworkkey.IsStoredArtworkKey(object.Key) {
				return true, nil
			}
		}
		if done {
			return false, nil
		}
		if next == "" || next == cursor {
			return false, errors.New("artworkstore: s3 listing did not advance while proving authoritative emptiness")
		}
		cursor = next
	}
}

func (s *S3Store) readSentinel(ctx context.Context, key string) ([]byte, error) {
	body, err := s.client.GetObjectStream(ctx, s.bucket, key)
	if err != nil {
		if errors.Is(err, s3client.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("artworkstore: read s3 sentinel: %w", err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, maxMarkerFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMarkerFileBytes {
		return nil, errors.New("artworkstore: s3 sentinel is implausibly large")
	}
	return data, nil
}

func (s *S3Store) createSentinel(ctx context.Context, key string, data []byte) error {
	creator, ok := s.client.(conditionalS3Creator)
	if !ok {
		// Test/legacy adapters without conditional creation still converge by
		// reading the value after the write. Production uses If-None-Match.
		if err := s.client.PutObject(ctx, s.bucket, key, data); err != nil {
			return err
		}
		return nil
	}
	_, err := creator.PutObjectIfAbsent(ctx, s.bucket, key, data)
	return err
}
