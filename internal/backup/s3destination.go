package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// DestinationS3 is a bucket used only for backups (backup.s3.*). Every object
// written there is encrypted.
const DestinationS3 = "s3"

// S3Config is the dedicated backup bucket.
type S3Config struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket"`
	KeyPrefix string `json:"key_prefix,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"-"`
	PathStyle bool   `json:"path_style,omitempty"`
}

// Valid reports whether c names a bucket and an endpoint.
func (c S3Config) Valid() bool {
	return strings.TrimSpace(c.Bucket) != "" && strings.TrimSpace(c.Endpoint) != ""
}

// Client opens an S3 client for c.
func (c S3Config) Client() *s3client.Client {
	return s3client.NewClient(s3client.BucketConfig{
		Role: "backup", Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket,
		KeyPrefix: c.KeyPrefix, AccessKey: c.AccessKey, SecretKey: c.SecretKey, PathStyle: c.PathStyle,
	})
}

// S3Destination writes backups into a dedicated bucket. Keys are relative to
// the bucket's key prefix, as at every destination.
type S3Destination struct {
	client *s3client.Client
	// PartSize is the multipart part size of the objects Put writes. A
	// backup sets it from the previous backup's size so an object never
	// needs more parts than S3 allows.
	PartSize int64
}

// NewS3Destination opens the bucket c names.
func NewS3Destination(c S3Config) *S3Destination {
	return &S3Destination{client: c.Client()}
}

// defaultPartSize is s3client's own part size, 8 MiB: 10,000 of them hold
// about 80 GB.
const defaultPartSize = 8 << 20

// maxPartSize is the largest part S3 accepts.
const maxPartSize = 5 << 30

// unknownSizePartSize is the part size when no earlier backup says how
// large objects get: 10,000 parts of 64 MiB hold about 640 GiB. Parts are
// sent one at a time, so it is also the most an upload buffers.
const unknownSizePartSize = 64 << 20

// partSizeFor is the part size that holds an object of twice expected bytes
// in 9,000 parts, leaving room for growth, rounded up to a whole MiB and
// never below the default. With nothing to go by (a first backup, or one
// after every earlier one failed) it is unknownSizePartSize, so a large
// installation's first backup can complete.
func partSizeFor(expected int64) int64 {
	if expected <= 0 {
		return unknownSizePartSize
	}
	need := (2*expected + 8_999) / 9_000
	need = (need + (1<<20 - 1)) &^ (1<<20 - 1)
	return min(max(need, defaultPartSize), maxPartSize)
}

func (d *S3Destination) Name() string { return DestinationS3 }

func (d *S3Destination) Encrypted() bool { return true }

func (d *S3Destination) withPartSize(size int64) Destination {
	sized := *d
	sized.PartSize = size
	return &sized
}

func (d *S3Destination) bucket() string { return d.client.Bucket() }

func (d *S3Destination) Put(ctx context.Context, key string, r io.Reader) error {
	if err := blobstore.ValidateKey(key); err != nil {
		return err
	}
	return d.client.PutObjectStreamParts(ctx, d.bucket(), key, r, "application/octet-stream", d.PartSize)
}

func (d *S3Destination) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	body, info, err := d.client.GetObjectStreamInfo(ctx, d.bucket(), key)
	if errors.Is(err, s3client.ErrNotFound) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	return body, info.SizeBytes, nil
}

func (d *S3Destination) List(ctx context.Context, prefix string) ([]blobstore.ObjectInfo, error) {
	infos, err := d.client.ListObjectInfos(ctx, d.bucket(), prefix)
	if err != nil {
		return nil, err
	}
	out := make([]blobstore.ObjectInfo, 0, len(infos))
	for _, info := range infos {
		obj := blobstore.ObjectInfo{Key: info.Key, Size: info.SizeBytes, ETag: info.ETag}
		if info.LastModified != nil {
			obj.ModTime = *info.LastModified
		}
		out = append(out, obj)
	}
	return out, nil
}

// ErrDeleteRefused reports a bucket that would not delete a backup, such as
// one with object lock or a policy that denies deletes. Rotation records it
// as a warning and leaves retention to the bucket's lifecycle rules.
var ErrDeleteRefused = errors.New("the bucket refused to delete it; its lifecycle rules decide when it goes")

// DeletePrefix deletes a backup folder with its manifest first: a bucket
// that refuses deletes then leaves the backup whole, and a delete that stops
// partway leaves an incomplete folder the sweep finishes, never a manifest
// that lists objects that are gone.
func (d *S3Destination) DeletePrefix(ctx context.Context, prefix string) error {
	if err := checkDeletePrefix(prefix); err != nil {
		return err
	}
	if err := d.deleteKeys(ctx, []string{prefix + ManifestName}); err != nil {
		return err
	}
	keys, err := d.client.ListObjects(ctx, d.bucket(), prefix)
	if err != nil {
		return err
	}
	return d.deleteKeys(ctx, keys)
}

func (d *S3Destination) Delete(ctx context.Context, keys ...string) error {
	if err := checkDeleteKeys(keys); err != nil {
		return err
	}
	return d.deleteKeys(ctx, keys)
}

func (d *S3Destination) deleteKeys(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := d.client.DeleteObjects(ctx, d.bucket(), keys)
	if err != nil && ctx.Err() == nil && deleteRefused(err) {
		return fmt.Errorf("%w (%w)", ErrDeleteRefused, err)
	}
	return err
}

// deleteRefused recognizes a delete the bucket denied, as opposed to one
// that could not reach it. s3client reports per-key failures by code only.
func deleteRefused(err error) bool {
	if code := s3client.ErrorCode(err); code == "AccessDenied" || code == "ObjectLocked" || code == "InvalidRequest" {
		return true
	}
	if s3client.HTTPStatus(err) == http.StatusForbidden {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "AccessDenied") || strings.Contains(msg, "ObjectLocked")
}

func (d *S3Destination) Probe(ctx context.Context) error {
	return d.client.HeadBucket(ctx, d.bucket())
}

func (d *S3Destination) FreeBytes(context.Context) (uint64, bool, error) { return 0, false, nil }

func (d *S3Destination) DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, time.Time, bool, error) {
	url, expires, err := d.client.PresignGetURLAt(ctx, d.bucket(), key, time.Now(), ttl, 0)
	if err != nil {
		return "", time.Time{}, true, err
	}
	return url, expires, true, nil
}

func (d *S3Destination) Location(key string) string {
	if prefix := d.client.KeyPrefix(); prefix != "" {
		key = prefix + "/" + key
	}
	return "s3://" + d.bucket() + "/" + key
}

// S3 connection test steps, in order. A failed test names the first one
// that failed.
const (
	S3StepEndpoint    = "endpoint"
	S3StepCredentials = "credentials"
	S3StepBucket      = "bucket"
	S3StepPermission  = "permission"
)

// S3TestResult is the outcome of a connection test.
type S3TestResult struct {
	OK bool `json:"ok"`
	// Step is the step that failed: one of the S3Step constants.
	Step    string `json:"step,omitempty"`
	Message string `json:"message"`
}

// probeKey is the object a connection test writes and deletes.
const probeKey = RootPrefix + ".silo-connection-test"

// TestS3 checks a bucket the way backups use it: it reaches the bucket
// (HeadBucket), then writes a probe object under the key prefix and deletes
// it. It reports the first step that failed, never the backend's free-form
// message, which can echo credentials.
func TestS3(ctx context.Context, c S3Config) S3TestResult {
	if strings.TrimSpace(c.Endpoint) == "" {
		return S3TestResult{Step: S3StepEndpoint, Message: "Enter the bucket's endpoint."}
	}
	if strings.TrimSpace(c.Bucket) == "" {
		return S3TestResult{Step: S3StepBucket, Message: "Enter the bucket's name."}
	}
	client := c.Client()
	if err := client.HeadBucket(ctx, c.Bucket); err != nil {
		return s3TestFailure(err, S3StepBucket)
	}
	if err := client.PutObject(ctx, c.Bucket, probeKey, []byte("Silo connection test. Safe to delete.\n")); err != nil {
		return s3TestFailure(err, S3StepPermission)
	}
	if err := client.DeleteObject(ctx, c.Bucket, probeKey); err != nil {
		result := s3TestFailure(err, S3StepPermission)
		if result.Step == S3StepPermission {
			// A bucket that keeps objects until its own lifecycle rules
			// expire them refuses deletes; backups still work there, and
			// rotation records the deletes it was refused (rotate).
			return S3TestResult{OK: true, Message: "Connected. Silo wrote a test file but this bucket refuses deletes, so old backups stay until the bucket's lifecycle rules remove them."}
		}
		return result
	}
	return S3TestResult{OK: true, Message: "Connected. Silo wrote and deleted a test file."}
}

// s3TestFailure names what an error says failed. step is the step that was
// running, used when the error itself does not say.
func s3TestFailure(err error, step string) S3TestResult {
	code, status := s3client.ErrorCode(err), s3client.HTTPStatus(err)
	switch {
	case status == 0 && code == "":
		return S3TestResult{Step: S3StepEndpoint, Message: "Couldn't reach the endpoint. Check the address and that this server can reach it."}
	case code == "InvalidAccessKeyId", code == "SignatureDoesNotMatch", code == "InvalidToken", code == "ExpiredToken", code == "AuthorizationHeaderMalformed":
		return S3TestResult{Step: S3StepCredentials, Message: "The bucket rejected the access key or secret key (" + code + ")."}
	case code == "NoSuchBucket", status == http.StatusNotFound:
		return S3TestResult{Step: S3StepBucket, Message: "No bucket with that name exists at this endpoint."}
	case code == "PermanentRedirect", code == "IllegalLocationConstraintException", status == http.StatusMovedPermanently:
		return S3TestResult{Step: S3StepBucket, Message: "The bucket is in another region. Check the region, or turn path-style addressing on or off."}
	case status == http.StatusForbidden || code == "AccessDenied":
		if step == S3StepBucket {
			return S3TestResult{Step: S3StepCredentials, Message: "The bucket refused these keys. Check the access key and secret key, and that they can reach this bucket."}
		}
		return S3TestResult{Step: S3StepPermission, Message: "These keys can reach the bucket but not write to it under this folder."}
	}
	detail := code
	if detail == "" {
		detail = fmt.Sprintf("HTTP %d", status)
	}
	if step == S3StepBucket {
		return S3TestResult{Step: S3StepBucket, Message: "Couldn't open the bucket (" + detail + ")."}
	}
	return S3TestResult{Step: S3StepPermission, Message: "Couldn't write a test file (" + detail + ")."}
}
