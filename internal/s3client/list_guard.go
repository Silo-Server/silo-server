package s3client

import (
	"context"
	"errors"
	"net/http"

	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// maxListResponseBytes bounds the bytes read from one ListObjectsV2 response.
// A full page of 1000 maximum-length keys with worst-case XML escaping and
// every optional field measures about 8.3 MiB. The cap bounds bytes read, not
// decoded memory: a page of tiny entries can still decode to a few hundred
// MiB, against no bound at all before.
const maxListResponseBytes = 16 << 20

// boundListResponses stops an S3 endpoint that ignores MaxKeys, or pads its
// reply, from making the client read an unbounded list response.
func boundListResponses(stack *middleware.Stack) error {
	if stack.ID() != "ListObjectsV2" {
		return nil
	}
	// After the deserializer means closer to the transport: this middleware
	// wraps the raw body before the deserializer reads it.
	return stack.Deserialize.Insert(middleware.DeserializeMiddlewareFunc("SiloCapListBody", func(ctx context.Context, in middleware.DeserializeInput, next middleware.DeserializeHandler) (middleware.DeserializeOutput, middleware.Metadata, error) {
		out, metadata, err := next.HandleDeserialize(ctx, in)
		if resp, ok := out.RawResponse.(*smithyhttp.Response); ok && resp.Body != nil {
			resp.Body = http.MaxBytesReader(nil, resp.Body, maxListResponseBytes)
		}
		return out, metadata, err
	}), "OperationDeserializer", middleware.After)
}

// isOversizedResponse reports a response the client refused to read in full.
// Retrying would only read the same oversized reply again.
func isOversizedResponse(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}
