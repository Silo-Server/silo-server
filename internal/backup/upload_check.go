package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// checkUploadParts checks the upload's parts. When the admin sent a
// SHA-256, the parts are hashed whole first (the stored bytes, encrypted
// or not) and compared with it, and only a match is decoded, so nothing of
// an upload that does not match what was sent is decoded; without one, the
// set is decoded unverified, under the reader's bounds alone.
func (s *Service) checkUploadParts(ctx context.Context, keys []string, declared string) (*RestoreSet, *UploadRejection, error) {
	if declared != "" {
		parts := s.openParts(ctx, keys)
		h := sha256.New()
		_, err := io.Copy(h, parts)
		_ = parts.Close()
		switch {
		case parts.err != nil:
			return nil, nil, parts.err
		case err != nil:
			return nil, nil, err
		case hex.EncodeToString(h.Sum(nil)) != declared:
			return nil, &UploadRejection{Reason: RejectChecksum, Message: msgUploadChecksum}, nil
		}
	}
	parts := s.openParts(ctx, keys)
	defer func() { _ = parts.Close() }()
	return s.checkUpload(ctx, parts, declared)
}

// msgUploadChecksum rejects an upload whose bytes are not the ones sent.
const msgUploadChecksum = "The file that arrived does not match the SHA-256 sent when the upload started."

// checkUpload reads an upload through the restore-set reader, decrypting
// one encrypted with this server's key. It returns the set, or why the
// upload is rejected, or an error when the bytes could not be read, which
// rejects nothing. The admin's upload is checked whole here; it is read
// again, for a preview or a restore, only with their confirmation (an
// upload is unauthenticated).
func (s *Service) checkUpload(ctx context.Context, parts *partsReader, declared string) (*RestoreSet, *UploadRejection, error) {
	body, err := s.openUploaded(parts)
	switch {
	case parts.err != nil:
		return nil, nil, parts.err
	case errors.Is(err, ErrWrongKey):
		return nil, &UploadRejection{Reason: RejectEncrypted, Message: EncryptedUploadMessage}, nil
	case err != nil:
		return nil, &UploadRejection{Reason: RejectNotRestoreSet, Message: truncateRejection(err.Error())}, nil
	}
	rs, err := readStoredRestoreSet(ctx, body, ReadOptions{}, "", "")
	switch {
	case parts.err != nil:
		return nil, nil, parts.err
	case ctx.Err() != nil:
		return nil, nil, context.Cause(ctx)
	case errors.Is(err, ErrNotEnoughTempSpace):
		// Nothing is wrong with the upload: it stays, to be checked again
		// once there is room for its account documents.
		return nil, nil, err
	case errors.Is(err, ErrRestoreSetTooNew):
		return nil, &UploadRejection{Reason: RejectNewerSilo, Message: ErrRestoreSetTooNew.Error()}, nil
	case err != nil:
		return nil, &UploadRejection{Reason: RejectNotRestoreSet, Message: truncateRejection(err.Error())}, nil
	case declared != "" && declared != rs.Digest:
		// Changed between the hash and the read.
		rs.Cleanup()
		return nil, &UploadRejection{Reason: RejectChecksum, Message: msgUploadChecksum}, nil
	}
	return rs, nil, nil
}

func truncateRejection(message string) string { return truncateText(message, maxRejectionMessage) }
