package apiv2

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
)

const (
	artworkParamQuery   = "query"
	artworkRangeHeader  = "Range"
	artworkIntegerType  = "integer"
	artworkBinaryFormat = "binary"
)

type ArtworkRepairService interface {
	EnqueueArtworkRepair(context.Context, []string, int) (int, error)
}

// NewArtworkHandler shares the signed asset protocol with secondary listeners.
// It serves only artwork bytes; it does not mount native business operations.
func NewArtworkHandler(store blobstore.Store, signer *artworkurl.Signer, repair ArtworkRepairService) http.Handler {
	reg := &Registry{deps: Dependencies{ArtworkStore: store, ArtworkSigner: signer, ArtworkRepair: repair}}
	return http.HandlerFunc(reg.serveArtwork)
}

func registerArtwork(reg *Registry) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		id := "getArtwork"
		if method == http.MethodHead {
			id = "headArtwork"
		}
		op := Operation{Operation: humaOp(method, Prefix+"/artwork/{key}", id, "artwork", "Read cached artwork."), Class: ClassPublic, ServiceBacked: true}
		op.Parameters = []*huma.Param{
			{Name: fieldKey, In: paramInPath, Required: true, Description: "Logical artwork key including its nested path.", Schema: &huma.Schema{Type: huma.TypeString}},
			{Name: "exp", In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: artworkIntegerType, Format: "int64"}},
			{Name: "sig", In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: huma.TypeString}},
			{Name: ifNoneMatchField, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}},
			{Name: artworkRangeHeader, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}},
		}
		responses := map[string]*huma.Response{"200": {Description: "Artwork bytes"}, "206": {Description: "Partial artwork bytes"}, "304": {Description: "Artwork not modified"}, "404": {Description: "Artwork not found"}, "503": {Description: "Artwork storage unavailable"}}
		if method == http.MethodGet {
			responses["200"].Content = map[string]*huma.MediaType{"image/*": {Schema: &huma.Schema{Type: huma.TypeString, Format: artworkBinaryFormat}}}
			responses["206"].Content = responses["200"].Content
		}
		op.Responses = responses
		RegisterRaw(reg, RawOperation{Operation: op, WildcardParam: fieldKey, Protocol: "artwork-image", Reason: "Artwork bytes and range semantics bypass JSON encoding."}, http.HandlerFunc(reg.serveArtwork))
	}
}

func (reg *Registry) serveArtwork(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, Prefix+"/artwork/")
	if reg.deps.ArtworkStore == nil || reg.deps.ArtworkSigner == nil {
		writeProblem(w, r, NewProblem(TypeNotFound, "Artwork not found."))
		return
	}
	if err := blobstore.ValidateKey(key); err != nil {
		writeProblem(w, r, NewProblem(TypeNotFound, "Artwork not found."))
		return
	}
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil {
		writeProblem(w, r, NewProblem(TypeNotFound, "Artwork not found."))
		return
	}
	if err := reg.deps.ArtworkSigner.Verify(key, exp, r.URL.Query().Get("sig"), time.Now()); err != nil {
		writeProblem(w, r, NewProblem(TypeNotFound, "Artwork not found."))
		return
	}
	reader, info, err := reg.deps.ArtworkStore.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			if artworkkey.Revision(key) != "" && reg.deps.ArtworkRepair != nil {
				_, _ = reg.deps.ArtworkRepair.EnqueueArtworkRepair(r.Context(), []string{originalRepairKey(key)}, 1)
			}
			writeProblem(w, r, NewProblem(TypeNotFound, "Artwork not found."))
			return
		}
		writeProblem(w, r, unavailable("artwork storage"))
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", blobstore.MediaType(key))
	if info.ETag != "" {
		w.Header().Set("ETag", info.ETag)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// A revisioned key never changes content, so it may be cached for the
	// URL's remaining lifetime. A mutable key (an avatar, library poster, or
	// collection image replaced in place) keeps its URL within the issuance
	// bucket, so clients must revalidate; the ETag turns that into a 304.
	cache := cacheControlPrivateNoCache
	if artworkkey.Revision(key) != "" {
		seconds := max(exp-time.Now().Unix(), 0)
		cache = "private, max-age=" + strconv.FormatInt(seconds, 10) + ", immutable"
	}
	w.Header().Set("Cache-Control", cache)
	if r.Header.Get(ifNoneMatchField) != "" && r.Header.Get(ifNoneMatchField) == info.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	content, ok := reader.(io.ReadSeeker)
	if !ok {
		// Object storage streams are forward-only. ServeContent only needs the
		// size and a forward seek to a single range start, so wrap the stream
		// rather than buffer the object. A multi-range request would seek
		// backwards; answer it with the whole object, which HTTP permits.
		if strings.Contains(r.Header.Get(artworkRangeHeader), ",") {
			r.Header.Del(artworkRangeHeader)
		}
		content = &forwardSeeker{reader: reader, size: info.Size}
	}
	http.ServeContent(w, r, path.Base(key), info.ModTime, content)
}

// forwardSeeker adapts a forward-only stream of known size to io.ReadSeeker
// for http.ServeContent, which seeks to the end for the size and then forward
// to the requested offset. Skipped bytes are read and discarded.
type forwardSeeker struct {
	reader io.Reader
	size   int64
	pos    int64
}

func (f *forwardSeeker) Read(p []byte) (int, error) {
	n, err := f.reader.Read(p)
	f.pos += int64(n)
	return n, err
}

func (f *forwardSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = f.pos + offset
	case io.SeekEnd:
		target = f.size + offset
	default:
		return 0, errors.New("artwork: invalid seek")
	}
	if target < 0 {
		return 0, errors.New("artwork: negative seek")
	}
	if target == f.size {
		// ServeContent reads the size this way; nothing has to be consumed.
		return target, nil
	}
	if target < f.pos {
		return 0, errors.New("artwork: backward seek on a stream")
	}
	if _, err := io.CopyN(io.Discard, f.reader, target-f.pos); err != nil {
		return 0, err
	}
	f.pos = target
	return target, nil
}

func originalRepairKey(key string) string {
	dir := artworkkey.Directory(key)
	if dir == "" {
		return key
	}
	base := path.Base(key)
	ext := path.Ext(base)
	stem := base[:len(base)-len(ext)]
	dot := strings.IndexByte(stem, '.')
	if dot < 0 {
		return key
	}
	return dir + "original" + stem[dot:] + ext
}
