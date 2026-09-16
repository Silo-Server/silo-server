package s3client

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ListObjectInfosPage makes one bounded storage request. The continuation token
// is opaque and must be reused with the same bucket and prefix. An empty result
// can still have a continuation token, for example after namespace filtering.
func (c *Client) ListObjectInfosPage(ctx context.Context, bucket, prefix, token string, limit int) ([]ObjectInfo, string, error) {
	if limit < 1 || limit > 1000 {
		return nil, "", fmt.Errorf("object page limit must be between 1 and 1000")
	}
	in := &s3.ListObjectsV2Input{Bucket: new(bucket), Prefix: new(c.prefixedKey(prefix)), MaxKeys: new(int32(limit))}
	if token != "" {
		in.ContinuationToken = new(token)
	}
	page, err := c.s3Client.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("listing object page: %w", err)
	}
	objects := make([]ObjectInfo, 0, len(page.Contents))
	for _, obj := range page.Contents {
		if obj.Key == nil {
			continue
		}
		key, ok := c.stripKeyPrefix(*obj.Key)
		if !ok {
			continue
		}
		objects = append(objects, ObjectInfo{Key: key, SizeBytes: aws.ToInt64(obj.Size), LastModified: obj.LastModified})
	}
	next := ""
	if aws.ToBool(page.IsTruncated) {
		next = aws.ToString(page.NextContinuationToken)
		if next == "" || next == token {
			return nil, "", fmt.Errorf("object storage returned an invalid continuation token")
		}
	}
	return objects, next, nil
}

// ListObjectInfosAfter lists a bounded page after a logical key. Unlike the
// opaque continuation-token API, this cursor survives deletion of earlier keys.
func (c *Client) ListObjectInfosAfter(ctx context.Context, bucket, prefix, after string, limit int) ([]ObjectInfo, string, error) {
	if limit < 1 || limit > 1000 {
		return nil, "", fmt.Errorf("object page limit must be between 1 and 1000")
	}
	in := &s3.ListObjectsV2Input{Bucket: new(bucket), Prefix: new(c.prefixedKey(prefix)), MaxKeys: new(int32(limit))}
	if after != "" {
		in.StartAfter = new(c.prefixedKey(after))
	}
	page, err := c.s3Client.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("listing objects after key: %w", err)
	}
	objects := make([]ObjectInfo, 0, len(page.Contents))
	for _, obj := range page.Contents {
		if obj.Key == nil {
			continue
		}
		key, ok := c.stripKeyPrefix(*obj.Key)
		if !ok {
			continue
		}
		objects = append(objects, ObjectInfo{Key: key, SizeBytes: aws.ToInt64(obj.Size), LastModified: obj.LastModified, ETag: aws.ToString(obj.ETag)})
	}
	next := ""
	if aws.ToBool(page.IsTruncated) {
		if len(objects) == 0 || objects[len(objects)-1].Key <= after {
			return nil, "", fmt.Errorf("object storage returned a nonadvancing key page")
		}
		next = objects[len(objects)-1].Key
	}
	return objects, next, nil
}
