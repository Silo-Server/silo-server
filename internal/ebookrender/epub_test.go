package ebookrender_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ebookrender"
)

func epub(t *testing.T, overrides map[string]string) []byte {
	t.Helper()
	files := map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"OPS/book.opf":           `<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><manifest><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="one"/></spine></package>`,
		"OPS/one.xhtml":          `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Hidden</title></head><body><p>Hello <em>世界</em>.</p><p>Next.</p></body></html>`,
	}
	for k, v := range overrides {
		files[k] = v
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadEPUBInSpineOrderAndPaginateUnicode(t *testing.T) {
	data := epub(t, nil)
	book, err := ebookrender.Parse(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Chapters) != 1 || book.Chapters[0].Text != "Hello 世界.\nNext." {
		t.Fatalf("chapters: %#v", book.Chapters)
	}
	pages, err := book.Pages(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Text != "Hello 世界" || pages[1].Text != ".\nNext." {
		t.Fatalf("pages: %#v", pages)
	}
	if pages[0].Location == pages[1].Location {
		t.Fatal("locations collide")
	}
	again, _ := book.Pages(4)
	if again[0].Location != pages[0].Location {
		t.Fatal("location depends on page size")
	}
}

func TestRejectUnsafeAndUnsupportedEPUB(t *testing.T) {
	cases := map[string]map[string]string{
		"archive traversal":           {"../escape": "no"},
		"absolute archive path":       {"/escape": "no"},
		"encrypted":                   {"META-INF/encryption.xml": "<encryption/>"},
		"manifest external":           {"OPS/book.opf": `<package xmlns="http://www.idpf.org/2007/opf"><manifest><item id="one" href="https://example.invalid/a"/></manifest><spine><itemref idref="one"/></spine></package>`},
		"duplicate manifest":          {"OPS/book.opf": `<package xmlns="http://www.idpf.org/2007/opf"><manifest/><manifest><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="one"/></spine></package>`},
		"wrong manifest namespace":    {"OPS/book.opf": `<package xmlns="http://www.idpf.org/2007/opf"><manifest xmlns="urn:wrong"><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="one"/></spine></package>`},
		"spine missing":               {"OPS/book.opf": `<package xmlns="http://www.idpf.org/2007/opf"><manifest/><spine><itemref idref="missing"/></spine></package>`},
		"aliased spine amplification": {"OPS/book.opf": `<package xmlns="http://www.idpf.org/2007/opf"><manifest><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/><item id="alias" href="one.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="one"/><itemref idref="alias"/></spine></package>`},
		"repeated spine":              {"OPS/book.opf": `<package xmlns="http://www.idpf.org/2007/opf"><manifest><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="one"/><itemref idref="one"/></spine></package>`},
		"malformed XHTML":             {"OPS/one.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>bad</body></html>`},
		"multiple roots":              {"OPS/one.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body/></html><html xmlns="http://www.w3.org/1999/xhtml"/>`},
		"DTD":                         {"OPS/one.xhtml": `<!DOCTYPE html [<!ENTITY e SYSTEM "file:///etc/passwd">]><html><body>&e;</body></html>`},
		"external image":              {"OPS/one.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body><img src="https://example.invalid/a"/></body></html>`},
		"fixed layout":                {"OPS/book.opf": `<package xmlns="http://www.idpf.org/2007/opf"><metadata><meta property="rendition:layout">pre-paginated</meta></metadata><manifest/><spine/></package>`},
		"deep XML":                    {"OPS/one.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body>` + strings.Repeat("<a>", 129) + strings.Repeat("</a>", 129) + `</body></html>`},
		"large entry":                 {"unused": strings.Repeat("a", ebookrender.MaxEntryBytes+1)},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			data := epub(t, override)
			if _, err := ebookrender.Parse(context.Background(), bytes.NewReader(data), int64(len(data))); err == nil {
				t.Fatal("unsafe EPUB accepted")
			}
		})
	}
}

func TestCanceledAndOversizedInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := epub(t, nil)
	if _, err := ebookrender.Parse(ctx, bytes.NewReader(data), int64(len(data))); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error: %v", err)
	}
	if _, err := ebookrender.Parse(context.Background(), bytes.NewReader(nil), ebookrender.MaxArchiveBytes+1); !errors.Is(err, ebookrender.ErrLimit) {
		t.Fatalf("size error: %v", err)
	}
}

func TestRejectDuplicateZIPEntriesAndExpandedBudget(t *testing.T) {
	for _, duplicate := range []bool{true, false} {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		for i := 0; i < 9; i++ {
			name := fmt.Sprintf("part%d", i)
			if duplicate {
				name = "same"
			}
			f, err := w.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Write([]byte(strings.Repeat("x", ebookrender.MaxEntryBytes))); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := ebookrender.Parse(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len())); err == nil {
			t.Fatalf("accepted duplicate=%v", duplicate)
		}
	}
}

func TestSpineOrderAndIgnoredExecutableContent(t *testing.T) {
	data := epub(t, map[string]string{
		"OPS/book.opf":  `<package xmlns="http://www.idpf.org/2007/opf"><manifest><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/><item id="two" href="two.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="two"/><itemref idref="one"/></spine></package>`,
		"OPS/two.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body><script>bad()</script><style>hidden</style><p>First.</p></body></html>`,
	})
	book, err := ebookrender.Parse(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Chapters) != 2 || book.Chapters[0].Text != "First." || book.Chapters[1].Path != "OPS/one.xhtml" {
		t.Fatalf("chapters: %#v", book.Chapters)
	}
}

func TestPageLimits(t *testing.T) {
	book := ebookrender.Book{Chapters: []ebookrender.Chapter{{Text: strings.Repeat("x", ebookrender.MaxPages+1)}}}
	for _, size := range []int{0, 16385, 1} {
		if _, err := book.Pages(size); !errors.Is(err, ebookrender.ErrLimit) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
}

func TestRejectEntryFloodBeforeZIPReaderAllocation(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for i := 0; i < ebookrender.MaxEntries+1; i++ {
		if _, err := w.Create(fmt.Sprintf("entry%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	// Lie about the entry count: a guard must count actual directory records.
	end := len(data) - 22
	binary.LittleEndian.PutUint16(data[end+8:], 1)
	binary.LittleEndian.PutUint16(data[end+10:], 1)
	if _, err := ebookrender.Parse(context.Background(), bytes.NewReader(data), int64(len(data))); !errors.Is(err, ebookrender.ErrLimit) {
		t.Fatalf("entry flood: %v", err)
	}
}

func TestXMLDefensesAfterValidXHTMLRoot(t *testing.T) {
	cases := []struct {
		text string
		want error
	}{
		{`<html xmlns="http://www.w3.org/1999/xhtml"><body>` + strings.Repeat("<span>", 129) + strings.Repeat("</span>", 129) + `</body></html>`, ebookrender.ErrLimit},
		{`<html xmlns="http://www.w3.org/1999/xhtml"><body/></html><html xmlns="http://www.w3.org/1999/xhtml"/>`, ebookrender.ErrInvalid},
	}
	for _, tc := range cases {
		data := epub(t, map[string]string{"OPS/one.xhtml": tc.text})
		if _, err := ebookrender.Parse(context.Background(), bytes.NewReader(data), int64(len(data))); !errors.Is(err, tc.want) {
			t.Fatalf("want %v got %v", tc.want, err)
		}
	}
}

func TestAllHeadingLevelsKeepTextSeparate(t *testing.T) {
	data := epub(t, map[string]string{"OPS/one.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body><h4>Four</h4><h5>Five</h5><h6>Six</h6>After</body></html>`})
	book, err := ebookrender.Parse(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if book.Chapters[0].Text != "Four\nFive\nSix\nAfter" {
		t.Fatalf("headings merged: %q", book.Chapters[0].Text)
	}
}

func TestBlockquoteKeepsSurroundingTextSeparate(t *testing.T) {
	for _, quote := range []string{"Quoted", "<p>Quoted</p>"} {
		t.Run(quote, func(t *testing.T) {
			data := epub(t, map[string]string{"OPS/one.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><body>Before<blockquote>` + quote + `</blockquote>After</body></html>`})
			book, err := ebookrender.Parse(t.Context(), bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if book.Chapters[0].Text != "Before\nQuoted\nAfter" {
				t.Fatalf("blockquote merged: %q", book.Chapters[0].Text)
			}
		})
	}
}

func TestRejectAmbiguousEndRecordInsideZIPComment(t *testing.T) {
	inner := epub(t, nil)
	original := epub(t, nil)
	// The genuine EOCD comment reaches EOF, but the nested archive's later EOCD
	// has a zero comment length with trailing bytes. archive/zip selects the latter.
	end := len(original) - 22
	comment := append(append([]byte(nil), inner...), []byte("tail")...)
	binary.LittleEndian.PutUint16(original[end+20:], uint16(len(comment)))
	data := append(original, comment...)
	if _, err := ebookrender.Parse(context.Background(), bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("accepted ambiguous directory end records")
	}
}

func TestRejectZIP64DiscoverySizeSentinel(t *testing.T) {
	original := epub(t, nil)
	end := len(original) - 22
	directoryBytes := int(binary.LittleEndian.Uint32(original[end+12 : end+16]))
	zr, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range zr.File {
		if err := w.Copy(f); err != nil {
			t.Fatal(err)
		}
	}
	// archive/zip treats 0xffff (not only 0xffffffff) as a ZIP64 discovery
	// sentinel for directory size. Reject it even without a ZIP64 locator.
	if _, err := w.Create(strings.Repeat("x", 0xffff-directoryBytes-46)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	if got := binary.LittleEndian.Uint32(data[len(data)-10 : len(data)-6]); got != 0xffff {
		t.Fatalf("fixture directory size: %d", got)
	}
	if _, err := ebookrender.Parse(context.Background(), bytes.NewReader(data), int64(len(data))); !errors.Is(err, ebookrender.ErrUnsupported) {
		t.Fatalf("ZIP64 discovery sentinel: %v", err)
	}
}

// Cancellation must stop archive reads before allocating the compressed input.
func TestCanceledParseDoesNotReadArchive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &countingReaderAt{}
	_, err := ebookrender.Parse(ctx, reader, 32)
	if !errors.Is(err, context.Canceled) || reader.reads != 0 {
		t.Fatalf("error %v, reads %d", err, reader.reads)
	}
}

type countingReaderAt struct{ reads int }

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	return 0, errors.New("unexpected read")
}
