package httpstream

import (
	"io"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/andybalholm/brotli/matchfinder"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

// Levels were chosen by encoding shape-faithful v2 JSON bodies of 1.3 MB,
// 130 kB, 36 kB and 1.5 kB. Against the earlier standard-library gzip at
// level 5, on bodies over 30 kB: zstd's default level is 1-25% smaller and
// 2.5-3x faster; brotli level 3 on the block encoder is 3-27% smaller and
// 2-2.7x faster; klauspost gzip at level 6 is within 1.4% of the size at
// 30-65% less CPU. Every encoder below keeps at most a few MB of state
// whatever the body size, which bounds memory to that per response being
// encoded at once.
const (
	gzipLevel = 6
	// brotliLevel selects NewWriterV2's block encoder (ZM matchfinder, 1 MiB
	// window); levels 4 and 5 cost 1.3-3x the CPU for under 4% smaller output.
	brotliLevel = 3
	// brotliBlock is the input the block encoder takes at a time, and how
	// much brotliWriter holds back before it commits to that encoder.
	brotliBlock = 1 << 16
	// The streaming encoder serves only responses that flush, which are
	// progressive subtitle extracts flushed every 32 KiB.
	brotliStreamQuality = 4
	brotliStreamLGWin   = 18
	// zstdWindow bounds the encoder's history. RFC 9659 caps a zstd content
	// coding's window at 8 MiB; the encoder's 8 MiB default holds about
	// 19 MB per encoder, and 512 KiB holds about 3 MB with the same output on
	// these bodies.
	zstdWindow = 1 << 19
)

const (
	codingZstd   = "zstd"
	codingBrotli = "br"
	codingGzip   = "gzip"
)

// codings lists the content codings the compressor offers, in the order it
// prefers them when a client weights several equally: zstd has the best ratio
// per CPU, brotli the smallest output, and gzip is what every client decodes.
var codings = [...]*coding{
	{name: codingZstd, newEncoder: func() encoder { return newZstdEncoder() }},
	{name: codingBrotli, newEncoder: func() encoder { return newBrotliEncoder() }},
	{name: codingGzip, alias: "x-gzip", newEncoder: func() encoder { return newGzipEncoder() }},
}

type coding struct {
	name       string
	alias      string
	newEncoder func() encoder
	pool       sync.Pool
}

// encoder is the streaming interface every coding's writer provides. Reset
// readies a pooled encoder for a new body written to dst.
type encoder interface {
	io.WriteCloser
	Flush() error
	Reset(dst io.Writer)
}

func (c *coding) get(dst io.Writer) encoder {
	e, ok := c.pool.Get().(encoder)
	if !ok {
		e = c.newEncoder()
	}
	e.Reset(dst)
	return e
}

func (c *coding) put(e encoder) { c.pool.Put(e) }

func newGzipEncoder() *gzip.Writer {
	w, err := gzip.NewWriterLevel(nil, gzipLevel)
	if err != nil {
		panic(err)
	}
	return w
}

func newZstdEncoder() *zstd.Encoder {
	e, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		// One synchronous encoder per response: no goroutines per stream.
		zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(zstdWindow),
		zstd.WithLowerEncoderMem(true),
		// An empty body still needs a complete frame to decode.
		zstd.WithZeroFrames(true))
	if err != nil {
		panic(err)
	}
	return e
}

func newBrotliEncoder() *brotliWriter {
	return &brotliWriter{block: brotli.NewWriterV2(nil, brotliLevel)}
}

var brotliStreams sync.Pool

// brotliWriter encodes with brotli's block encoder, which is fast and keeps
// its memory bounded whatever the body size, but cannot end a block early to
// flush. It therefore holds back the first block of input: a body that ends
// without a flush, which is every buffered API response, goes through the
// block encoder; a response that flushes first, such as a progressive
// subtitle extract, switches to the streaming encoder, which can. A flush
// after the first block has gone to the block encoder sends nothing early;
// the rest follows block by block and at Close.
type brotliWriter struct {
	dst       io.Writer
	held      []byte
	block     *matchfinder.Writer
	blockMode bool
	stream    *brotli.Writer
}

func (b *brotliWriter) Reset(dst io.Writer) {
	b.dst, b.held, b.blockMode, b.stream = dst, b.held[:0], false, nil
}

func (b *brotliWriter) Write(p []byte) (int, error) {
	switch {
	case b.stream != nil:
		return b.stream.Write(p)
	case b.blockMode:
		return b.block.Write(p)
	case len(b.held)+len(p) <= brotliBlock:
		b.held = append(b.held, p...)
		return len(p), nil
	}
	if err := b.startBlocks(); err != nil {
		return 0, err
	}
	return b.block.Write(p)
}

func (b *brotliWriter) startBlocks() error {
	b.block.Reset(b.dst)
	b.blockMode = true
	held := b.held
	b.held = b.held[:0]
	if len(held) == 0 {
		return nil
	}
	_, err := b.block.Write(held)
	return err
}

func (b *brotliWriter) Flush() error {
	switch {
	case b.stream != nil:
		return b.stream.Flush()
	case b.blockMode || len(b.held) == 0:
		return nil
	}
	stream, ok := brotliStreams.Get().(*brotli.Writer)
	if !ok {
		stream = brotli.NewWriterOptions(nil, brotli.WriterOptions{Quality: brotliStreamQuality, LGWin: brotliStreamLGWin})
	}
	b.stream = stream
	b.stream.Reset(b.dst)
	_, err := b.stream.Write(b.held)
	b.held = b.held[:0]
	if err != nil {
		return err
	}
	return b.stream.Flush()
}

func (b *brotliWriter) Close() error {
	if b.stream != nil {
		err := b.stream.Close()
		brotliStreams.Put(b.stream)
		b.stream, b.dst = nil, nil
		return err
	}
	if !b.blockMode {
		if err := b.startBlocks(); err != nil {
			return err
		}
	}
	b.dst = nil
	return b.block.Close()
}
