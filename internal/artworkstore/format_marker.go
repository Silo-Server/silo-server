package artworkstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

const (
	formatMarkerFileName = ".silo-artwork-format"
	formatMarkerContents = "silo-artwork-format-v1\n"
)

func (s *FilesystemStore) EnsureFormatMarker(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, release, err := s.openRoot()
	if err != nil {
		return err
	}
	defer release()
	file, _, err := openRegular(root, formatMarkerFileName)
	if err == nil {
		defer func() { _ = file.Close() }()
		data, readErr := io.ReadAll(io.LimitReader(file, 128))
		if readErr != nil || string(data) != formatMarkerContents {
			return errors.New("artworkstore: artwork format marker is invalid")
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, ErrNotFound) {
		return err
	}
	created, err := root.OpenFile(formatMarkerFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, storeFilePerm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return s.EnsureFormatMarker(ctx)
		}
		return fmt.Errorf("artworkstore: create format marker: %w", err)
	}
	if _, err := created.Write([]byte(formatMarkerContents)); err != nil {
		_ = created.Close()
		_ = root.Remove(formatMarkerFileName)
		return err
	}
	if err := created.Sync(); err != nil {
		_ = created.Close()
		_ = root.Remove(formatMarkerFileName)
		return err
	}
	return created.Close()
}

func (s *FilesystemStore) HasFormatMarker(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, release, err := s.openRootExisting()
	if err != nil {
		return err
	}
	defer release()
	file, _, err := openRegular(root, formatMarkerFileName)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 128))
	if err != nil || string(data) != formatMarkerContents {
		return errors.New("artworkstore: artwork format marker is invalid")
	}
	return nil
}
