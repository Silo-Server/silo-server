// Package ebookrender extracts bounded plain text from DRM-free reflowable EPUB.
package ebookrender

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

const (
	MaxArchiveBytes  = 32 << 20
	MaxEntries       = 1024
	MaxEntryBytes    = 4 << 20
	MaxExpandedBytes = 32 << 20
	MaxPages         = 10000
	xmlPackage       = "package"
	xmlManifest      = "manifest"
	xmlSpine         = "spine"
)

var (
	ErrInvalid     = errors.New("invalid EPUB")
	ErrLimit       = errors.New("EPUB limit exceeded")
	ErrUnsupported = errors.New("unsupported EPUB")
)

type Chapter struct {
	Path string
	Text string
}
type Book struct {
	Revision string
	Chapters []Chapter
}
type Page struct {
	Text     string
	Location string
}

// Pages splits at Unicode scalar boundaries. Locations identify normalized text
// offsets, not EPUB CFI, and must not overwrite existing browser reader progress.
// Locations include the extraction version and an archive digest.
func (b *Book) Pages(runesPerPage int) ([]Page, error) {
	if runesPerPage < 1 || runesPerPage > 16384 {
		return nil, fmt.Errorf("%w: page size", ErrLimit)
	}
	var pages []Page
	for i, ch := range b.Chapters {
		text := []rune(ch.Text)
		for offset := 0; offset < len(text); offset += runesPerPage {
			if len(pages) >= MaxPages {
				return nil, ErrLimit
			}
			end := min(offset+runesPerPage, len(text))
			pages = append(pages, Page{Text: string(text[offset:end]), Location: fmt.Sprintf("silo-text-v1:%s:%d:%d", b.Revision, i, offset)})
		}
	}
	return pages, nil
}

// Parse reads an entire bounded archive in memory; callers should additionally
// bound concurrent jobs. The context is checked during reads and XML tokenization.
func Parse(ctx context.Context, r io.ReaderAt, size int64) (*Book, error) {
	raw, files, err := readArchive(ctx, r, size)
	if err != nil {
		return nil, err
	}
	opf, pkg, err := readPackage(ctx, files)
	if err != nil {
		return nil, err
	}
	items, err := manifestTargets(pkg, opf, files)
	if err != nil {
		return nil, err
	}
	book, err := extractChapters(ctx, pkg, items, files)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	book.Revision = hex.EncodeToString(sum[:])
	return book, nil
}

func readArchive(ctx context.Context, r io.ReaderAt, size int64) ([]byte, map[string][]byte, error) {
	if size <= 0 {
		return nil, nil, ErrInvalid
	}
	if size > MaxArchiveBytes {
		return nil, nil, ErrLimit
	}
	raw, err := io.ReadAll(withContext(ctx, io.NewSectionReader(r, 0, size)))
	if err != nil {
		return nil, nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err = preflightZIP(raw); err != nil {
		return nil, nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), size)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: zip", ErrInvalid)
	}
	if len(zr.File) > MaxEntries {
		return nil, nil, ErrLimit
	}
	files := make(map[string][]byte, len(zr.File))
	var total uint64
	for _, f := range zr.File {
		if err = ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := strings.TrimSuffix(f.Name, "/")
		if !safePath(name) {
			return nil, nil, fmt.Errorf("%w: archive path", ErrInvalid)
		}
		if _, ok := files[f.Name]; ok {
			return nil, nil, fmt.Errorf("%w: duplicate path", ErrInvalid)
		}
		if !f.Mode().IsRegular() && !f.FileInfo().IsDir() {
			return nil, nil, ErrUnsupported
		}
		if f.UncompressedSize64 > MaxEntryBytes {
			return nil, nil, ErrLimit
		}
		total += f.UncompressedSize64
		if total > MaxExpandedBytes {
			return nil, nil, ErrLimit
		}
		rc, e := f.Open()
		if e != nil {
			return nil, nil, fmt.Errorf("%w: entry", ErrInvalid)
		}
		data, e := io.ReadAll(withContext(ctx, io.LimitReader(rc, MaxEntryBytes+1)))
		closeErr := rc.Close()
		if e != nil || closeErr != nil {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			return nil, nil, fmt.Errorf("%w: entry", ErrInvalid)
		}
		if len(data) > MaxEntryBytes {
			return nil, nil, ErrLimit
		}
		files[f.Name] = data
	}
	if string(files["mimetype"]) != "application/epub+zip" {
		return nil, nil, ErrUnsupported
	}
	if _, ok := files["META-INF/encryption.xml"]; ok {
		return nil, nil, ErrUnsupported
	}
	return raw, files, nil
}

type packageDocument struct {
	XMLName xml.Name `xml:"http://www.idpf.org/2007/opf package"`
	Items   []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		Type       string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		ID         string `xml:"idref,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"spine>itemref"`
	Meta []struct {
		Property string `xml:"property,attr"`
		Value    string `xml:",chardata"`
	} `xml:"metadata>meta"`
}

func readPackage(ctx context.Context, files map[string][]byte) (string, *packageDocument, error) {
	var container struct {
		XMLName xml.Name `xml:"urn:oasis:names:tc:opendocument:xmlns:container container"`
		Roots   []struct {
			Path string `xml:"full-path,attr"`
			Type string `xml:"media-type,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := decode(ctx, files["META-INF/container.xml"], &container); err != nil {
		return "", nil, err
	}
	if len(container.Roots) != 1 || container.Roots[0].Type != "application/oebps-package+xml" || !safePath(container.Roots[0].Path) {
		return "", nil, ErrInvalid
	}
	opf := container.Roots[0].Path
	var pkg packageDocument
	if err := validatePackage(ctx, files[opf]); err != nil {
		return "", nil, err
	}
	if err := decode(ctx, files[opf], &pkg); err != nil {
		return "", nil, err
	}
	for _, m := range pkg.Meta {
		if m.Property == "rendition:layout" && strings.TrimSpace(m.Value) == "pre-paginated" {
			return "", nil, ErrUnsupported
		}
	}
	return opf, &pkg, nil
}

func manifestTargets(pkg *packageDocument, opf string, files map[string][]byte) (map[string]string, error) {
	items := map[string]string{}
	for _, item := range pkg.Items {
		if item.ID == "" {
			return nil, ErrInvalid
		}
		if _, ok := items[item.ID]; ok {
			return nil, ErrInvalid
		}
		if !safePath(item.Href) {
			return nil, ErrUnsupported
		}
		target := path.Join(path.Dir(opf), item.Href)
		if _, ok := files[target]; !ok {
			return nil, ErrInvalid
		}
		if strings.Contains(item.Properties, "remote-resources") {
			return nil, ErrUnsupported
		}
		items[item.ID] = target
	}
	return items, nil
}

func extractChapters(ctx context.Context, pkg *packageDocument, items map[string]string, files map[string][]byte) (*Book, error) {
	if len(pkg.Spine) == 0 {
		return nil, ErrInvalid
	}
	book := &Book{}
	seenSpine := map[string]bool{}
	seenTargets := map[string]bool{}
	for _, ref := range pkg.Spine {
		if seenSpine[ref.ID] {
			return nil, ErrInvalid
		}
		seenSpine[ref.ID] = true
		target, ok := items[ref.ID]
		if !ok {
			return nil, ErrInvalid
		}
		if seenTargets[target] {
			return nil, ErrInvalid
		}
		seenTargets[target] = true
		for _, item := range pkg.Items {
			if item.ID == ref.ID && item.Type != "application/xhtml+xml" {
				return nil, ErrUnsupported
			}
		}
		if strings.Contains(ref.Properties, "rendition:layout-pre-paginated") {
			return nil, ErrUnsupported
		}
		text, e := chapterText(ctx, files[target])
		if e != nil {
			return nil, e
		}
		book.Chapters = append(book.Chapters, Chapter{Path: target, Text: text})
	}
	return book, nil
}

func safePath(s string) bool {
	u, err := url.Parse(s)
	return err == nil && s != "" && !strings.ContainsAny(s, "\\%\x00?#:") && !strings.HasPrefix(s, "/") && path.Clean(s) == s && s != "." && s != ".." && !strings.HasPrefix(s, "../") && u.Scheme == "" && u.Host == ""
}

// tokens enforces a depth bound and rejects DTD/entity declarations and processing
// instructions other than the XML declaration. encoding/xml never resolves entities.
func tokens(ctx context.Context, data []byte, visit func(xml.Token) error) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	roots := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		t, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalid
		}
		switch v := t.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
			if depth > 128 {
				return ErrLimit
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return ErrUnsupported
		case xml.ProcInst:
			if v.Target != "xml" {
				return ErrUnsupported
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(v)) != "" {
				return ErrInvalid
			}
		}
		if visit != nil {
			if err = visit(t); err != nil {
				return err
			}
		}
	}
	if roots != 1 || depth != 0 {
		return ErrInvalid
	}
	return nil
}
func decode(ctx context.Context, data []byte, out any) error {
	if err := tokens(ctx, data, nil); err != nil {
		return err
	}
	if err := xml.Unmarshal(data, out); err != nil {
		return ErrInvalid
	}
	return nil
}

func chapterText(ctx context.Context, data []byte) (string, error) {
	var out strings.Builder
	body := false
	seenBody := false
	depth := 0
	bodyDepth := 0
	skip := 0
	root := false
	block := func() {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
	}
	err := tokens(ctx, data, func(token xml.Token) error {
		switch v := token.(type) {
		case xml.StartElement:
			depth++
			if !root {
				root = true
				if v.Name.Local != "html" || v.Name.Space != "http://www.w3.org/1999/xhtml" {
					return ErrUnsupported
				}
			}
			if err := validateResourceReferences(v); err != nil {
				return err
			}
			if v.Name.Local == "body" {
				if seenBody {
					return ErrInvalid
				}
				seenBody = true
				body = true
				bodyDepth = depth
			}
			if v.Name.Local == "script" || v.Name.Local == "style" {
				skip++
			}
			if body && skip == 0 {
				if blockElement(v.Name.Local) || v.Name.Local == "br" {
					block()
				}
			}
		case xml.EndElement:
			if body && skip == 0 {
				if blockElement(v.Name.Local) {
					block()
				}
			}
			if v.Name.Local == "script" || v.Name.Local == "style" {
				skip--
			}
			if depth == bodyDepth {
				body = false
			}
			depth--
		case xml.CharData:
			if body && skip == 0 {
				out.Write(v)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if !seenBody {
		return "", ErrInvalid
	}
	lines := strings.Split(out.String(), "\n")
	var clean []string
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			clean = append(clean, line)
		}
	}
	return strings.Join(clean, "\n"), nil
}

// validatePackage prevents local-name XML decoding from accepting namespace
// spoofing or merging duplicate package sections.
func validatePackage(ctx context.Context, data []byte) error {
	var stack []string
	manifest, spine := 0, 0
	err := tokens(ctx, data, func(t xml.Token) error {
		switch v := t.(type) {
		case xml.StartElement:
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			switch v.Name.Local {
			case xmlPackage, xmlManifest, xmlSpine, "item", "itemref":
				if v.Name.Space != "http://www.idpf.org/2007/opf" {
					return ErrInvalid
				}
			}
			switch v.Name.Local {
			case xmlManifest:
				if parent != xmlPackage {
					return ErrInvalid
				}
				manifest++
			case xmlSpine:
				if parent != xmlPackage {
					return ErrInvalid
				}
				spine++
			case "item":
				if parent != xmlManifest {
					return ErrInvalid
				}
			case "itemref":
				if parent != xmlSpine {
					return ErrInvalid
				}
			}
			stack = append(stack, v.Name.Local)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
		return nil
	})
	if err != nil {
		return err
	}
	if manifest != 1 || spine != 1 {
		return ErrInvalid
	}
	return nil
}

// preflightZIP counts actual central-directory records before archive/zip can
// allocate a File for each one. EOCD counts alone are attacker-controlled and
// archive/zip deliberately compares only their low 16 bits. This bounded EPUB
// subset admits single-disk ordinary ZIP only (no ZIP64 or prepended executable).
func preflightZIP(raw []byte) error {
	const endSize = 22
	end := -1
	for i := len(raw) - endSize; i >= max(0, len(raw)-endSize-65535); i-- {
		if binary.LittleEndian.Uint32(raw[i:i+4]) == 0x06054b50 {
			end = i
			break
		}
	}
	// Reject even a truncated later signature: archive/zip may select a later
	// candidate whose comment merely fits rather than reaching EOF. Requiring
	// the last signature to reach EOF binds both parsers to this directory.
	if end < 0 || end+endSize+int(binary.LittleEndian.Uint16(raw[end+20:end+22])) != len(raw) {
		return ErrInvalid
	}
	e := raw[end:]
	if binary.LittleEndian.Uint16(e[4:6]) != 0 || binary.LittleEndian.Uint16(e[6:8]) != 0 {
		return ErrUnsupported
	}
	count := int(binary.LittleEndian.Uint16(e[10:12]))
	if count > MaxEntries {
		return ErrLimit
	}
	if int(binary.LittleEndian.Uint16(e[8:10])) != count {
		return ErrInvalid
	}
	length := uint64(binary.LittleEndian.Uint32(e[12:16]))
	offset := uint64(binary.LittleEndian.Uint32(e[16:20]))
	// Match archive/zip's ZIP64 discovery sentinels, including its 16-bit
	// directory-size sentinel; no locator may redirect the checked directory.
	if length == 0xffff || length == 0xffffffff || offset == 0xffffffff {
		return ErrUnsupported
	}
	if length > MaxArchiveBytes || length > uint64(end) || offset > uint64(end) || offset+length != uint64(end) {
		return ErrInvalid
	}
	actual := 0
	for pos := end - int(length); pos < end; {
		if end-pos < 46 || binary.LittleEndian.Uint32(raw[pos:pos+4]) != 0x02014b50 {
			return ErrInvalid
		}
		actual++
		if actual > MaxEntries {
			return ErrLimit
		}
		name := int(binary.LittleEndian.Uint16(raw[pos+28 : pos+30]))
		extra := int(binary.LittleEndian.Uint16(raw[pos+30 : pos+32]))
		comment := int(binary.LittleEndian.Uint16(raw[pos+32 : pos+34]))
		if binary.LittleEndian.Uint16(raw[pos+34:pos+36]) != 0 {
			return ErrUnsupported
		}
		next := pos + 46 + name + extra + comment
		if next > end {
			return ErrInvalid
		}
		pos = next
	}
	if actual != count {
		return ErrInvalid
	}
	return nil
}

// A caller-provided ReaderAt can still block inside one read.
type readerFunc func([]byte) (int, error)

func (r readerFunc) Read(p []byte) (int, error) { return r(p) }
func withContext(ctx context.Context, reader io.Reader) io.Reader {
	return readerFunc(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return reader.Read(p)
	})
}

func blockElement(name string) bool {
	switch name {
	case "p", "div", "blockquote", "li", "h1", "h2", "h3", "h4", "h5", "h6", "section":
		return true
	default:
		return false
	}
}

func validateResourceReferences(element xml.StartElement) error {
	for _, attribute := range element.Attr {
		switch attribute.Name.Local {
		case "href", "src", "poster", "data":
			if attribute.Value != "" && !strings.HasPrefix(attribute.Value, "#") && !safePath(attribute.Value) {
				return ErrUnsupported
			}
		}
	}
	return nil
}
