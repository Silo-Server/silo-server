// Package s3client provides a thin wrapper around the AWS SDK v2 S3 client
// that works with any S3-compatible backend (CephRGW, MinIO, etc.).
// It uses only standard S3 APIs and supports per-bucket credentials via
// BucketConfig.
package s3client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrNotFound is returned when the requested S3 object does not exist.
var ErrNotFound = errors.New("s3: object not found")

// URL auth strategies for generating read URLs.
const (
	URLAuthPresigned       = "presigned"        // standard S3 presigned URLs (default)
	URLAuthPublic          = "public"           // unsigned public URLs via custom domain
	URLAuthCloudflareToken = "cloudflare_token" // Cloudflare WAF token authentication
)

// BucketConfig holds the configuration for connecting to a single S3 bucket.
// Each bucket may have different credentials and endpoints, allowing per-bucket
// configuration for metadata, operational, and user-db buckets.
type BucketConfig struct {
	Endpoint       string
	PublicEndpoint string // optional: public CDN domain for reads (e.g. R2 custom domain)
	Region         string
	Bucket         string
	KeyPrefix      string
	AccessKey      string
	SecretKey      string
	PathStyle      bool
	URLAuth        string // "presigned" (default) or "cloudflare_token"
	TokenSecret    string // HMAC-SHA256 secret for Cloudflare token auth
	TokenParam     string // query param name (default: "verify")
	TokenTTL       int    // token lifetime in seconds (default: 10800 = 3h)
}

// Client wraps an AWS SDK v2 S3 client configured for a specific bucket.
type Client struct {
	s3Client       *s3.Client
	presignClient  *s3.PresignClient
	bucket         string
	keyPrefix      string
	endpoint       string
	publicEndpoint string
	pathStyle      bool
	urlAuth        string
	tokenSecret    string
	tokenParam     string
	tokenTTL       int
}

// ObjectInfo describes an object stored in S3.
type ObjectInfo struct {
	Key          string
	SizeBytes    int64
	LastModified *time.Time
}

// NewClient creates a new S3 Client from the given BucketConfig.
// The caller creates multiple Client instances for different buckets.
func NewClient(cfg BucketConfig) *Client {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	s3Client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKey,
			cfg.SecretKey,
			"",
		),
		UsePathStyle: cfg.PathStyle,
	})

	presignClient := s3.NewPresignClient(s3Client)

	tokenParam := cfg.TokenParam
	if tokenParam == "" {
		tokenParam = "verify"
	}
	tokenTTL := cfg.TokenTTL
	if tokenTTL <= 0 {
		tokenTTL = 10800 // 3 hours
	}
	keyPrefix := NormalizeKeyPrefix(cfg.KeyPrefix)

	return &Client{
		s3Client:       s3Client,
		presignClient:  presignClient,
		bucket:         cfg.Bucket,
		keyPrefix:      keyPrefix,
		endpoint:       cfg.Endpoint,
		publicEndpoint: cfg.PublicEndpoint,
		pathStyle:      cfg.PathStyle,
		urlAuth:        cfg.URLAuth,
		tokenSecret:    cfg.TokenSecret,
		tokenParam:     tokenParam,
		tokenTTL:       tokenTTL,
	}
}

// Bucket returns the bucket name this client is configured for.
func (c *Client) Bucket() string {
	return c.bucket
}

// GetObject fetches the object at the given key and returns its contents.
// Returns ErrNotFound if the object does not exist.
func (c *Client) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
	objectKey := c.prefixedKey(key)
	out, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isNotFoundErr(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("s3 GetObject %s/%s: %w", bucket, key, err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("s3 reading body %s/%s: %w", bucket, key, err)
	}

	return data, nil
}

// PutObject uploads data to the given key, inferring Content-Type from the
// file extension so that CDNs and browsers serve files with the correct MIME type.
func (c *Client) PutObject(ctx context.Context, bucket, key string, data []byte) error {
	body := newBytesReadSeeker(data)
	objectKey := c.prefixedKey(key)

	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
		Body:   body,
	}
	if ct := contentTypeFromKey(key); ct != "" {
		input.ContentType = aws.String(ct)
	}

	_, err := c.s3Client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("s3 PutObject %s/%s: %w", bucket, key, err)
	}

	return nil
}

// MakeObjectPublic updates the object ACL to allow anonymous reads.
func (c *Client) MakeObjectPublic(ctx context.Context, bucket, key string) error {
	objectKey := c.prefixedKey(key)
	_, err := c.s3Client.PutObjectAcl(ctx, &s3.PutObjectAclInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
		ACL:    s3types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		return fmt.Errorf("s3 PutObjectAcl %s/%s: %w", bucket, key, err)
	}
	return nil
}

// UploadFile uploads the file at path to the given key and returns the size in bytes.
func (c *Client) UploadFile(ctx context.Context, bucket, key, path, contentType string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening upload file %s: %w", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stating upload file %s: %w", path, err)
	}

	input := &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(c.prefixedKey(key)),
		Body:          file,
		ContentLength: aws.Int64(info.Size()),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	if _, err := c.s3Client.PutObject(ctx, input); err != nil {
		return 0, fmt.Errorf("s3 PutObject file %s/%s: %w", bucket, key, err)
	}

	return info.Size(), nil
}

// PresignGetURL generates a read URL for the given object. The strategy depends
// on the configured URLAuth:
//   - "cloudflare_token": HMAC-signed URL via the public endpoint
//   - "public": unsigned URL via the public endpoint
//   - "presigned" (default): standard S3 presigned URL
func (c *Client) PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error) {
	objectKey := c.prefixedKey(key)
	if c.publicEndpoint != "" {
		switch c.urlAuth {
		case URLAuthCloudflareToken:
			return c.cloudflareTokenURL(objectKey), nil
		case URLAuthPublic:
			return c.PublicURL(bucket, key)
		}
	}

	req, err := c.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("s3 PresignGetObject %s/%s: %w", bucket, key, err)
	}

	return req.URL, nil
}

// EffectivePresignTTL returns the longest validity window that the client can
// honor for a read URL generated with PresignGetURL.
func (c *Client) EffectivePresignTTL(requested time.Duration) time.Duration {
	if requested <= 0 {
		return requested
	}
	if c.urlAuth == URLAuthCloudflareToken && c.tokenTTL > 0 {
		tokenTTL := time.Duration(c.tokenTTL) * time.Second
		if tokenTTL < requested {
			return tokenTTL
		}
	}
	return requested
}

// cloudflareTokenURL generates a Cloudflare WAF token-authenticated URL.
// Format: {publicEndpoint}/{key}?{param}={timestamp}-{base64_hmac}
// The HMAC is SHA256(secret, "/{key}" + timestamp).
func (c *Client) cloudflareTokenURL(key string) string {
	path := "/" + key
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	mac := hmac.New(sha256.New, []byte(c.tokenSecret))
	mac.Write([]byte(path))
	mac.Write([]byte(ts))
	token := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return strings.TrimRight(c.publicEndpoint, "/") + path +
		"?" + c.tokenParam + "=" + ts + "-" + url.QueryEscape(token)
}

// UsesExternalAuth returns true if read URLs are served via a public endpoint
// (token auth or public bucket) rather than S3 presigned URLs.
func (c *Client) UsesExternalAuth() bool {
	return c.urlAuth == URLAuthCloudflareToken || c.urlAuth == URLAuthPublic
}

// PublicURL returns the deterministic public URL for an object based on the
// configured endpoint and path-style setting. When a public endpoint is set
// (e.g. an R2 custom domain), it is used instead of the API endpoint and the
// bucket prefix is omitted (the custom domain maps directly to the bucket).
func (c *Client) PublicURL(bucket, key string) (string, error) {
	objectKey := c.prefixedKey(key)

	// Public endpoint: domain is bound to the bucket, so just /{key}.
	if c.publicEndpoint != "" {
		baseURL, err := url.Parse(c.publicEndpoint)
		if err != nil {
			return "", fmt.Errorf("parse public endpoint %q: %w", c.publicEndpoint, err)
		}
		path := normalizedURLBasePath(baseURL.Path)
		path = appendURLObjectKey(path, objectKey)
		return buildPublicURL(baseURL, baseURL.Host, path), nil
	}

	// Standard S3 endpoint: include bucket per path-style setting.
	baseURL, err := url.Parse(c.endpoint)
	if err != nil {
		return "", fmt.Errorf("parse S3 endpoint %q: %w", c.endpoint, err)
	}
	if baseURL.Scheme == "" || baseURL.Host == "" {
		return "", fmt.Errorf("parse S3 endpoint %q: missing scheme or host", c.endpoint)
	}

	host := baseURL.Host
	path := normalizedURLBasePath(baseURL.Path)
	if c.pathStyle {
		path = appendURLPathSegment(path, bucket)
		path = appendURLObjectKey(path, objectKey)
		return buildPublicURL(baseURL, host, path), nil
	}

	host = bucket + "." + host
	path = appendURLObjectKey(path, objectKey)
	return buildPublicURL(baseURL, host, path), nil
}

// SetBucketCORS configures CORS on the bucket to allow browser GET requests
// from the given origins. This is required for presigned URL fetches from the frontend.
func (c *Client) SetBucketCORS(ctx context.Context, bucket string, allowedOrigins []string) error {
	_, err := c.s3Client.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket: aws.String(bucket),
		CORSConfiguration: &s3types.CORSConfiguration{
			CORSRules: []s3types.CORSRule{
				{
					AllowedOrigins: allowedOrigins,
					AllowedMethods: []string{"GET"},
					AllowedHeaders: []string{"*"},
					MaxAgeSeconds:  aws.Int32(86400),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("s3 PutBucketCors %s: %w", bucket, err)
	}
	return nil
}

// HeadBucket checks whether the configured bucket exists and is accessible.
// This is useful for readiness checks.
func (c *Client) HeadBucket(ctx context.Context, bucket string) error {
	_, err := c.s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return fmt.Errorf("s3 HeadBucket %s: %w", bucket, err)
	}

	return nil
}

// ObjectExists checks whether an object exists at the given key.
func (c *Client) ObjectExists(ctx context.Context, bucket, key string) (bool, error) {
	objectKey := c.prefixedKey(key)
	_, err := c.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, fmt.Errorf("s3 HeadObject %s/%s: %w", bucket, key, err)
	}

	return true, nil
}

// DeleteObject deletes the object at the given key.
func (c *Client) DeleteObject(ctx context.Context, bucket, key string) error {
	objectKey := c.prefixedKey(key)
	_, err := c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("s3 DeleteObject %s/%s: %w", bucket, key, err)
	}

	return nil
}

// DeletePrefix deletes all objects matching the given prefix using batch
// DeleteObjects (up to 1000 keys per request). Returns the number of objects
// deleted. Falls back to individual deletes if batch is not supported.
func (c *Client) DeletePrefix(ctx context.Context, bucket, prefix string) (int, error) {
	keys, err := c.ListObjects(ctx, bucket, prefix)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	return c.DeleteObjects(ctx, bucket, keys)
}

// DeleteObjects deletes the given keys in batches of up to 1000 (the S3 API
// limit). Returns the total number of successfully deleted objects.
func (c *Client) DeleteObjects(ctx context.Context, bucket string, keys []string) (int, error) {
	const batchSize = 1000
	deleted := 0

	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]

		objects := make([]s3types.ObjectIdentifier, len(batch))
		for j, key := range batch {
			objects[j] = s3types.ObjectIdentifier{Key: aws.String(c.prefixedKey(key))}
		}

		out, err := c.s3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &s3types.Delete{
				Objects: objects,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			// Fall back to individual deletes if batch is not supported.
			for _, key := range batch {
				if delErr := c.DeleteObject(ctx, bucket, key); delErr != nil {
					slog.WarnContext(ctx, "s3 DeleteObjects fallback: failed to delete", "component", "s3client", "key", key, "error", delErr)
					continue
				}
				deleted++
			}
			continue
		}

		deleted += len(batch)
		if out != nil {
			deleted -= len(out.Errors)
			for _, e := range out.Errors {
				slog.WarnContext(ctx, "s3 DeleteObjects: partial failure", "component", "s3client",
					"key", aws.ToString(e.Key), "code", aws.ToString(e.Code), "message", aws.ToString(e.Message))
			}
		}
	}

	return deleted, nil
}

// ListObjects lists all object keys with the given prefix.
func (c *Client) ListObjects(ctx context.Context, bucket, prefix string) ([]string, error) {
	objects, err := c.ListObjectInfos(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(objects))
	for _, obj := range objects {
		keys = append(keys, obj.Key)
	}

	return keys, nil
}

// ListObjectInfos lists objects with the given prefix and returns summary metadata.
func (c *Client) ListObjectInfos(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
	var objects []ObjectInfo
	prefixedPrefix := c.prefixedKey(prefix)

	paginator := s3.NewListObjectsV2Paginator(c.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefixedPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 ListObjectsV2 %s prefix=%s: %w", bucket, prefix, err)
		}

		for _, obj := range page.Contents {
			if obj.Key != nil {
				logicalKey, ok := c.stripKeyPrefix(*obj.Key)
				if !ok {
					continue
				}
				var size int64
				if obj.Size != nil {
					size = *obj.Size
				}
				objects = append(objects, ObjectInfo{
					Key:          logicalKey,
					SizeBytes:    size,
					LastModified: obj.LastModified,
				})
			}
		}
	}

	return objects, nil
}

// isNotFoundErr checks whether an error indicates that the object was not found.
func isNotFoundErr(err error) bool {
	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}

	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return true
	}

	// Some S3-compatible backends return a generic error with "NotFound" or
	// "NoSuchKey" in the message, or an HTTP 404 status. Check the smithy
	// response error interface.
	type httpResponseError interface {
		HTTPStatusCode() int
	}
	var httpErr httpResponseError
	if errors.As(err, &httpErr) {
		if httpErr.HTTPStatusCode() == 404 {
			return true
		}
	}

	return false
}

// bytesReadSeeker wraps a byte slice to implement io.ReadSeeker for PutObject.
type bytesReadSeeker struct {
	data   []byte
	offset int64
}

func newBytesReadSeeker(data []byte) *bytesReadSeeker {
	return &bytesReadSeeker{data: data}
}

// NormalizeKeyPrefix returns the canonical form of a bucket key prefix as the
// client applies it to object keys: whitespace- and slash-trimmed, case
// preserved (S3 keys are case-sensitive). Exported so storage-identity
// comparisons elsewhere normalize prefixes exactly the way this client does.
func NormalizeKeyPrefix(prefix string) string {
	return strings.Trim(strings.TrimSpace(prefix), "/")
}

func (c *Client) prefixedKey(key string) string {
	trimmed := strings.TrimLeft(key, "/")
	if c.keyPrefix == "" {
		return trimmed
	}
	if trimmed == "" {
		return c.keyPrefix
	}
	return c.keyPrefix + "/" + trimmed
}

func (c *Client) stripKeyPrefix(key string) (string, bool) {
	trimmed := strings.TrimLeft(key, "/")
	if c.keyPrefix == "" {
		return trimmed, true
	}
	if trimmed == c.keyPrefix {
		return "", true
	}
	prefix := c.keyPrefix + "/"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	return strings.TrimPrefix(trimmed, prefix), true
}

func normalizedURLBasePath(basePath string) string {
	if basePath == "" {
		return ""
	}
	return strings.TrimRight(basePath, "/")
}

func appendURLPathSegment(path string, segment string) string {
	trimmed := strings.Trim(segment, "/")
	if trimmed == "" {
		return path
	}
	if path == "" {
		return "/" + trimmed
	}
	return path + "/" + trimmed
}

func appendURLObjectKey(path string, key string) string {
	if key == "" {
		return path
	}
	if path == "" {
		return "/" + key
	}
	return path + "/" + key
}

func buildPublicURL(baseURL *url.URL, host, path string) string {
	var b strings.Builder
	b.WriteString(baseURL.Scheme)
	b.WriteString("://")
	if baseURL.User != nil {
		b.WriteString(baseURL.User.String())
		b.WriteString("@")
	}
	b.WriteString(host)
	b.WriteString(encodeURLPath(path))
	if baseURL.RawQuery != "" {
		b.WriteString("?")
		b.WriteString(baseURL.RawQuery)
	}
	if baseURL.Fragment != "" {
		b.WriteString("#")
		b.WriteString(baseURL.Fragment)
	}
	return b.String()
}

func encodeURLPath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func (b *bytesReadSeeker) Read(p []byte) (int, error) {
	if b.offset >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.offset:])
	b.offset += int64(n)
	return n, nil
}

func (b *bytesReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = b.offset + offset
	case io.SeekEnd:
		newOffset = int64(len(b.data)) + offset
	default:
		return 0, fmt.Errorf("bytesReadSeeker: invalid whence %d", whence)
	}

	if newOffset < 0 {
		return 0, fmt.Errorf("bytesReadSeeker: negative position %d", newOffset)
	}

	b.offset = newOffset
	return newOffset, nil
}

// contentTypeFromKey returns a MIME type based on the file extension, or empty
// string if unknown. This ensures CDNs and browsers serve files correctly.
func contentTypeFromKey(key string) string {
	switch {
	case strings.HasSuffix(key, ".jpg"), strings.HasSuffix(key, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(key, ".png"):
		return "image/png"
	case strings.HasSuffix(key, ".webp"):
		return "image/webp"
	case strings.HasSuffix(key, ".gif"):
		return "image/gif"
	case strings.HasSuffix(key, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(key, ".json"):
		return "application/json"
	case strings.HasSuffix(key, ".zip"):
		return "application/zip"
	default:
		return ""
	}
}
