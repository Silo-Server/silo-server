// Package s3test is an in-memory S3 server for tests: the subset of the API
// s3client uses for backups, path-style only. It stores objects in memory,
// ignores request signatures (so presigned URLs work), and can refuse
// deletes the way a bucket with object lock or a deny policy does.
package s3test

import (
	"bufio"
	"bytes"
	"crypto/md5" //nolint:gosec // ETags are MD5 in S3; nothing here is security
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Server is a fake S3 endpoint holding one or more buckets.
type Server struct {
	*httptest.Server

	mu      sync.Mutex
	buckets map[string]map[string]object
	uploads map[string]*upload
	nextID  int
	// RefuseDeletes answers every delete with AccessDenied.
	refuseDeletes bool
	// maxPart is the largest multipart part received.
	maxPart int64
	parts   int
}

type object struct {
	data     []byte
	modified time.Time
}

type upload struct {
	bucket, key string
	parts       map[int][]byte
}

// New starts a server with the named buckets, closed when the test ends.
func New(t testing.TB, buckets ...string) *Server {
	t.Helper()
	s := &Server{buckets: map[string]map[string]object{}, uploads: map[string]*upload{}}
	for _, b := range buckets {
		s.buckets[b] = map[string]object{}
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// RefuseDeletes makes every delete answer AccessDenied, or stops it.
func (s *Server) RefuseDeletes(refuse bool) {
	s.mu.Lock()
	s.refuseDeletes = refuse
	s.mu.Unlock()
}

// Object returns a stored object's bytes.
func (s *Server) Object(bucket, key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.buckets[bucket][key]
	return o.data, ok
}

// Keys lists a bucket's keys under prefix, sorted.
func (s *Server) Keys(bucket, prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.buckets[bucket] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Put stores an object directly.
func (s *Server) Put(bucket, key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buckets[bucket][key] = object{data: append([]byte(nil), data...), modified: time.Now()}
}

// Parts reports how many multipart parts arrived and the largest one.
func (s *Server) Parts() (count int, largest int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parts, s.maxPart
}

type s3Error struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code string) {
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(s3Error{Code: code, Message: code})
}

func writeXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(v)
}

func etag(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // see the import
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucketName, key, _ := strings.Cut(path, "/")
	q := r.URL.Query()
	s.mu.Lock()
	_, ok := s.buckets[bucketName]
	s.mu.Unlock()
	switch {
	case !ok:
		writeError(w, r, http.StatusNotFound, "NoSuchBucket")
	case key == "" && r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case key == "" && r.Method == http.MethodGet && q.Get("list-type") == "2":
		s.list(w, bucketName, q.Get("prefix"), q.Get("continuation-token"))
	case key == "" && r.Method == http.MethodPost && q.Has("delete"):
		s.deleteObjects(w, r, bucketName)
	case q.Has("uploads") || q.Has("uploadId"):
		s.multipart(w, r, bucketName, key)
	default:
		s.object(w, r, bucketName, key)
	}
}

// multipart serves the multipart upload calls: create, part, complete, and
// abort.
func (s *Server) multipart(w http.ResponseWriter, r *http.Request, bucketName, key string) {
	q := r.URL.Query()
	id := q.Get("uploadId")
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		s.mu.Lock()
		s.nextID++
		id = "upload-" + strconv.Itoa(s.nextID)
		s.uploads[id] = &upload{bucket: bucketName, key: key, parts: map[int][]byte{}}
		s.mu.Unlock()
		writeXML(w, struct {
			XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
			Bucket   string   `xml:"Bucket"`
			Key      string   `xml:"Key"`
			UploadID string   `xml:"UploadId"`
		}{Bucket: bucketName, Key: key, UploadID: id})
	case r.Method == http.MethodPut:
		data, err := readBody(r)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "IncompleteBody")
			return
		}
		n, _ := strconv.Atoi(q.Get("partNumber"))
		s.mu.Lock()
		u := s.uploads[id]
		if u != nil {
			u.parts[n] = data
			s.parts++
			s.maxPart = max(s.maxPart, int64(len(data)))
		}
		s.mu.Unlock()
		if u == nil {
			writeError(w, r, http.StatusNotFound, "NoSuchUpload")
			return
		}
		w.Header().Set("ETag", etag(data))
	case r.Method == http.MethodPost:
		_, _ = io.Copy(io.Discard, r.Body)
		s.mu.Lock()
		u := s.uploads[id]
		delete(s.uploads, id)
		var data []byte
		if u != nil {
			numbers := make([]int, 0, len(u.parts))
			for n := range u.parts {
				numbers = append(numbers, n)
			}
			sort.Ints(numbers)
			for _, n := range numbers {
				data = append(data, u.parts[n]...)
			}
			s.buckets[bucketName][key] = object{data: data, modified: time.Now()}
		}
		s.mu.Unlock()
		if u == nil {
			writeError(w, r, http.StatusNotFound, "NoSuchUpload")
			return
		}
		writeXML(w, struct {
			XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
			Bucket  string   `xml:"Bucket"`
			Key     string   `xml:"Key"`
			ETag    string   `xml:"ETag"`
		}{Bucket: bucketName, Key: key, ETag: etag(data)})
	case r.Method == http.MethodDelete:
		s.mu.Lock()
		delete(s.uploads, id)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, r, http.StatusNotImplemented, "NotImplemented")
	}
}

// object serves one object's put, get, head, and delete.
func (s *Server) object(w http.ResponseWriter, r *http.Request, bucketName, key string) {
	switch r.Method {
	case http.MethodPut:
		data, err := readBody(r)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "IncompleteBody")
			return
		}
		s.mu.Lock()
		s.buckets[bucketName][key] = object{data: data, modified: time.Now()}
		s.mu.Unlock()
		w.Header().Set("ETag", etag(data))
	case http.MethodGet, http.MethodHead:
		s.mu.Lock()
		o, found := s.buckets[bucketName][key]
		s.mu.Unlock()
		if !found {
			writeError(w, r, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
		w.Header().Set("Last-Modified", o.modified.UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", etag(o.data))
		if r.Method == http.MethodGet {
			_, _ = w.Write(o.data)
		}
	case http.MethodDelete:
		s.mu.Lock()
		refuse := s.refuseDeletes
		if !refuse {
			delete(s.buckets[bucketName], key)
		}
		s.mu.Unlock()
		if refuse {
			writeError(w, r, http.StatusForbidden, "AccessDenied")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, r, http.StatusNotImplemented, "NotImplemented")
	}
}

const listPageSize = 1000

func (s *Server) list(w http.ResponseWriter, bucket, prefix, token string) {
	keys := s.Keys(bucket, prefix)
	start := 0
	if token != "" {
		start = sort.SearchStrings(keys, token)
	}
	end := min(start+listPageSize, len(keys))
	type content struct {
		Key          string `xml:"Key"`
		Size         int    `xml:"Size"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
	}
	result := struct {
		XMLName               xml.Name  `xml:"ListBucketResult"`
		Name                  string    `xml:"Name"`
		Prefix                string    `xml:"Prefix"`
		KeyCount              int       `xml:"KeyCount"`
		IsTruncated           bool      `xml:"IsTruncated"`
		NextContinuationToken string    `xml:"NextContinuationToken,omitempty"`
		Contents              []content `xml:"Contents"`
	}{Name: bucket, Prefix: prefix}
	s.mu.Lock()
	for _, k := range keys[start:end] {
		o := s.buckets[bucket][k]
		result.Contents = append(result.Contents, content{Key: k, Size: len(o.data), LastModified: o.modified.UTC().Format(time.RFC3339), ETag: etag(o.data)})
	}
	s.mu.Unlock()
	result.KeyCount = len(result.Contents)
	if end < len(keys) {
		result.IsTruncated, result.NextContinuationToken = true, keys[end]
	}
	writeXML(w, result)
}

func (s *Server) deleteObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	data, err := readBody(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "MalformedXML")
		return
	}
	var req struct {
		Objects []struct {
			Key string `xml:"Key"`
		} `xml:"Object"`
	}
	if err := xml.Unmarshal(data, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "MalformedXML")
		return
	}
	type deleted struct {
		Key string `xml:"Key"`
	}
	type failed struct {
		Key     string `xml:"Key"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	result := struct {
		XMLName xml.Name  `xml:"DeleteResult"`
		Deleted []deleted `xml:"Deleted"`
		Errors  []failed  `xml:"Error"`
	}{}
	s.mu.Lock()
	for _, o := range req.Objects {
		if s.refuseDeletes {
			result.Errors = append(result.Errors, failed{Key: o.Key, Code: "AccessDenied", Message: "Access Denied"})
			continue
		}
		delete(s.buckets[bucket], o.Key)
		result.Deleted = append(result.Deleted, deleted{Key: o.Key})
	}
	s.mu.Unlock()
	writeXML(w, result)
}

// readBody reads a request body, decoding aws-chunked framing, which the
// SDK uses to send a streamed body with a trailing checksum.
func readBody(r *http.Request) ([]byte, error) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		return data, nil
	}
	return decodeAWSChunked(data)
}

// decodeAWSChunked strips aws-chunked framing: chunks of
// "<hex size>[;extensions]\r\n<bytes>\r\n", ended by a zero-size chunk and
// optional trailers.
func decodeAWSChunked(data []byte) ([]byte, error) {
	br := bufio.NewReader(bytes.NewReader(data))
	var out []byte
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("aws-chunked: %w", err)
		}
		sizeField, _, _ := strings.Cut(strings.TrimRight(line, "\r\n"), ";")
		size, err := strconv.ParseInt(strings.TrimSpace(sizeField), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("aws-chunked size %q: %w", sizeField, err)
		}
		if size == 0 {
			return out, nil
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(br, chunk); err != nil {
			return nil, fmt.Errorf("aws-chunked: %w", err)
		}
		out = append(out, chunk...)
		if crlf, err := br.ReadString('\n'); err != nil || strings.TrimRight(crlf, "\r\n") != "" {
			return nil, errors.New("aws-chunked: chunk not followed by CRLF")
		}
	}
}
