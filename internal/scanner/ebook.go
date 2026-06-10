package scanner

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/charmap"
	textunicode "golang.org/x/text/encoding/unicode"
)

const maxEPUBMetadataEntrySize = 8 * 1024 * 1024
const maxPDFMetadataScanSize = 2 * 1024 * 1024

var ebookExtensions = map[string]bool{
	".epub": true,
	".pdf":  true,
	".mobi": true,
	".azw":  true,
	".azw3": true,
	".fb2":  true,
	".fbz":  true,
	".cbz":  true,
	".cbr":  true,
}

type parsedEbook struct {
	Format      string
	Title       string
	Authors     []string
	Description string
	Publisher   string
	PublishedAt time.Time
	Year        int
	Language    string
	ISBN        string
	Series      string
	SeriesIndex string
	Genres      []string
	PageCount   int
	Cover       *parsedEbookCover
}

type parsedEbookCover struct {
	ContentType string
	Bytes       []byte
}

func SupportsEbookFile(filePath string) bool {
	if strings.HasSuffix(strings.ToLower(filePath), ".fb2.zip") {
		return true
	}
	return ebookExtensions[strings.ToLower(filepath.Ext(filePath))]
}

func parseEbookFile(path string) (book parsedEbook, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic parsing ebook %s: %v", path, r)
		}
	}()
	format := ebookFileFormat(path)
	switch format {
	case ".epub":
		book, err = parseEbookEPUB(path)
	case ".fb2":
		book, err = parseEbookFB2(path)
	case ".fbz":
		book, err = parseEbookFBZ(path)
	case ".cbz":
		book, err = parseEbookCBZ(path)
	case ".pdf":
		book, err = parseEbookPDF(path)
	case ".mobi", ".azw", ".azw3", ".cbr":
		book = parsedEbook{Format: strings.TrimPrefix(format, ".")}
	default:
		err = fmt.Errorf("unsupported ebook format: %s", filepath.Ext(path))
	}
	book.sanitize()
	return book, err
}

func parseEbookPDF(path string) (parsedEbook, error) {
	book := parsedEbook{Format: "pdf"}
	file, err := os.Open(path)
	if err != nil {
		return book, err
	}
	defer file.Close()

	head, tail, err := readPDFMetadataWindows(file)
	if err != nil {
		return book, err
	}
	info := parsePDFInfoFields(head)
	// A head match comes from a linearized PDF whose Info dictionary sits at
	// the start of the file and is authoritative; non-linearized PDFs (the
	// common case) store the Info dictionary near the end and usually have no
	// head match at all. The tail therefore only fills keys the head did not
	// produce, so key-shaped byte noise inside trailing compressed streams can
	// never replace a good head value.
	for key, value := range parsePDFInfoFields(tail) {
		if value != "" && info[key] == "" {
			info[key] = value
		}
	}
	book.Title = info["Title"]
	book.Authors = splitEbookAuthors(info["Author"])
	book.Description = info["Subject"]
	book.Genres = splitPDFKeywords(info["Keywords"])
	for _, value := range []string{
		info["ISBN"],
		info["Subject"],
		info["Keywords"],
		info["Title"],
	} {
		if isbn := normalizeEbookISBN(value); isbn != "" {
			book.ISBN = isbn
			break
		}
	}
	if t, ok := parsePDFDate(info["CreationDate"]); ok {
		book.PublishedAt = t
		book.Year = t.Year()
	}
	return book, nil
}

// readPDFMetadataWindows reads the first and last maxPDFMetadataScanSize bytes
// of the file. The windows never overlap: for files at most one window long
// the tail is nil, and for files shorter than two windows the tail starts
// where the head ends.
func readPDFMetadataWindows(file *os.File) (head []byte, tail []byte, err error) {
	head, err = io.ReadAll(io.LimitReader(file, maxPDFMetadataScanSize))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	size := info.Size()
	if size <= int64(len(head)) {
		return head, nil, nil
	}
	tailStart := size - maxPDFMetadataScanSize
	if tailStart < int64(len(head)) {
		tailStart = int64(len(head))
	}
	tail = make([]byte, size-tailStart)
	if _, err := file.ReadAt(tail, tailStart); err != nil {
		return nil, nil, err
	}
	return head, tail, nil
}

func ebookFileFormat(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".fb2.zip") {
		return ".fbz"
	}
	return strings.ToLower(filepath.Ext(path))
}

func (b *parsedEbook) sanitize() {
	b.Format = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(b.Format)), ".")
	b.Title = strings.TrimSpace(b.Title)
	b.Description = cleanEbookDescription(b.Description)
	b.Publisher = strings.TrimSpace(b.Publisher)
	b.Language = strings.TrimSpace(b.Language)
	b.ISBN = normalizeEbookISBN(b.ISBN)
	b.Series = strings.TrimSpace(b.Series)
	b.SeriesIndex = strings.TrimSpace(b.SeriesIndex)
	b.Authors = uniqueTrimmedStrings(b.Authors)
	b.Genres = uniqueTrimmedStrings(b.Genres)
	if b.PageCount < 0 {
		b.PageCount = 0
	}
	if b.Year == 0 && !b.PublishedAt.IsZero() {
		b.Year = b.PublishedAt.Year()
	}
}

func cleanEbookDescription(value string) string {
	value = strings.TrimSpace(html.UnescapeString(value))
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "<") || !strings.Contains(value, ">") {
		return strings.Join(strings.Fields(value), " ")
	}

	tokenizer := xhtml.NewTokenizer(strings.NewReader(value))
	var out strings.Builder
	needsSpace := false
	writeSpace := func() {
		if out.Len() > 0 && !needsSpace {
			needsSpace = true
		}
	}
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return strings.Join(strings.Fields(out.String()), " ")
		case xhtml.TextToken:
			text := strings.TrimSpace(html.UnescapeString(string(tokenizer.Text())))
			if text == "" {
				continue
			}
			if out.Len() > 0 && (needsSpace || !startsWithClosingPunctuation(text)) {
				out.WriteByte(' ')
			}
			out.WriteString(text)
			needsSpace = false
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken, xhtml.EndTagToken:
			name, _ := tokenizer.TagName()
			switch strings.ToLower(string(name)) {
			case "br", "p", "div", "section", "article", "li", "ul", "ol", "blockquote", "tr":
				writeSpace()
			}
		}
	}
}

func startsWithClosingPunctuation(value string) bool {
	for _, r := range value {
		switch r {
		case '.', ',', ';', ':', '!', '?', ')', ']', '}':
			return true
		default:
			return false
		}
	}
	return false
}

func looksLikeHTML(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, "<") && strings.Contains(value, ">")
}

func uniqueTrimmedStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		key := strings.ToLower(trimmed)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeEbookISBN(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	for _, prefix := range []string{"ISBN-13", "ISBN-10", "ISBN"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
			value = strings.TrimLeft(value, ": ")
			break
		}
	}
	var out strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			out.WriteRune(r)
			continue
		}
		if r == 'X' {
			out.WriteRune(r)
		}
	}
	candidate := out.String()
	switch len(candidate) {
	case 10:
		if validISBN10(candidate) {
			return candidate
		}
	case 13:
		if validISBN13(candidate) {
			return candidate
		}
	}
	return ""
}

func validISBN10(candidate string) bool {
	if len(candidate) != 10 {
		return false
	}
	sum := 0
	for i, r := range candidate {
		value := 0
		switch {
		case r >= '0' && r <= '9':
			value = int(r - '0')
		case r == 'X' && i == 9:
			value = 10
		default:
			return false
		}
		sum += value * (10 - i)
	}
	return sum%11 == 0
}

func validISBN13(candidate string) bool {
	if len(candidate) != 13 {
		return false
	}
	sum := 0
	for i, r := range candidate {
		if r < '0' || r > '9' {
			return false
		}
		value := int(r - '0')
		if i%2 == 1 {
			value *= 3
		}
		sum += value
	}
	return sum%10 == 0
}

func parseEbookEPUB(path string) (parsedEbook, error) {
	book := parsedEbook{Format: "epub"}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return book, err
	}
	defer reader.Close()

	container, err := readEPUBZipEntry(&reader.Reader, "META-INF/container.xml")
	if err != nil {
		return book, err
	}
	opfPath, err := epubOPFPath(container)
	if err != nil {
		return book, err
	}
	opf, err := readEPUBZipEntry(&reader.Reader, opfPath)
	if err != nil {
		return book, err
	}
	if err := parseEPUBOPFMetadata(opf, &book); err != nil {
		return book, err
	}
	if cover, err := extractEPUBCover(&reader.Reader, opfPath, opf); err == nil {
		book.Cover = cover
	}
	return book, nil
}

func parseEbookFB2(path string) (parsedEbook, error) {
	book := parsedEbook{Format: "fb2"}
	file, err := os.Open(path)
	if err != nil {
		return book, err
	}
	defer file.Close()
	// Mirror the .fbz entry cap so a plain .fb2 cannot stream unbounded
	// bytes through the XML decoder.
	info, err := file.Stat()
	if err != nil {
		return book, err
	}
	if info.Size() > maxEPUBMetadataEntrySize {
		return book, fmt.Errorf("fb2 file too large: %s", path)
	}
	return parseEbookFB2Reader(io.LimitReader(file, maxEPUBMetadataEntrySize+1), "fb2")
}

func parseEbookFBZ(path string) (parsedEbook, error) {
	book := parsedEbook{Format: "fbz"}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return book, err
	}
	defer reader.Close()

	for _, file := range reader.File {
		if !strings.HasSuffix(strings.ToLower(file.Name), ".fb2") {
			continue
		}
		if file.UncompressedSize64 > maxEPUBMetadataEntrySize {
			return book, fmt.Errorf("fbz entry too large: %s", file.Name)
		}
		entry, err := file.Open()
		if err != nil {
			return book, err
		}
		defer entry.Close()
		return parseEbookFB2Reader(io.LimitReader(entry, maxEPUBMetadataEntrySize+1), "fbz")
	}
	return book, fmt.Errorf("fbz archive has no fb2 entry")
}

func parseEbookCBZ(path string) (parsedEbook, error) {
	book := parsedEbook{Format: "cbz"}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return book, err
	}
	defer reader.Close()

	var coverPage *zip.File
	var coverKey string
	for _, file := range reader.File {
		if !isComicArchivePage(file.Name) {
			continue
		}
		book.PageCount++
		key := normalizedArchivePath(file.Name)
		if coverPage == nil || naturalPathLess(key, coverKey) {
			coverPage, coverKey = file, key
		}
	}
	if coverPage != nil {
		if cover, err := readArchiveImageCover(coverPage); err == nil {
			book.Cover = cover
		}
	}
	return book, nil
}

// naturalPathLess orders archive entry names case-insensitively with digit
// runs compared numerically, so unpadded page numbers ("2.jpg" before
// "10.jpg") and chapter directories ("ch2/" before "ch10/") sort in reading
// order instead of byte order.
func naturalPathLess(a, b string) bool {
	for len(a) > 0 && len(b) > 0 {
		if isASCIIDigit(a[0]) && isASCIIDigit(b[0]) {
			aRun, aRest := splitDigitRun(a)
			bRun, bRest := splitDigitRun(b)
			aNum := strings.TrimLeft(aRun, "0")
			bNum := strings.TrimLeft(bRun, "0")
			if len(aNum) != len(bNum) {
				return len(aNum) < len(bNum)
			}
			if aNum != bNum {
				return aNum < bNum
			}
			a, b = aRest, bRest
			continue
		}
		ar, aSize := utf8.DecodeRuneInString(a)
		br, bSize := utf8.DecodeRuneInString(b)
		al, bl := unicode.ToLower(ar), unicode.ToLower(br)
		if al != bl {
			return al < bl
		}
		a, b = a[aSize:], b[bSize:]
	}
	return len(a) < len(b)
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func splitDigitRun(s string) (run string, rest string) {
	i := 0
	for i < len(s) && isASCIIDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

func isComicArchivePage(name string) bool {
	clean := normalizedArchivePath(name)
	if clean == "" || strings.HasSuffix(clean, "/") {
		return false
	}
	base := strings.ToLower(filepath.Base(clean))
	if strings.HasPrefix(base, "._") {
		return false
	}
	parts := strings.Split(strings.ToLower(clean), "/")
	for _, part := range parts {
		if part == "__macosx" {
			return false
		}
	}
	switch filepath.Ext(base) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".bmp":
		return true
	default:
		return false
	}
}

func normalizedArchivePath(name string) string {
	return strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
}

func readArchiveImageCover(file *zip.File) (*parsedEbookCover, error) {
	if file == nil {
		return nil, fmt.Errorf("nil archive image")
	}
	if file.UncompressedSize64 > maxEPUBMetadataEntrySize {
		return nil, fmt.Errorf("archive cover entry too large: %s", file.Name)
	}
	entry, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer entry.Close()
	data, err := io.ReadAll(io.LimitReader(entry, maxEPUBMetadataEntrySize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxEPUBMetadataEntrySize {
		return nil, fmt.Errorf("archive cover entry too large: %s", file.Name)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("archive cover entry empty: %s", file.Name)
	}
	return &parsedEbookCover{
		ContentType: ebookImageContentType(file.Name),
		Bytes:       data,
	}, nil
}

func parseEbookFB2Reader(reader io.Reader, format string) (parsedEbook, error) {
	book := parsedEbook{Format: format}
	var fb2 struct {
		Description struct {
			TitleInfo struct {
				Genres  []string `xml:"genre"`
				Authors []struct {
					FirstName  string `xml:"first-name"`
					MiddleName string `xml:"middle-name"`
					LastName   string `xml:"last-name"`
					Nickname   string `xml:"nickname"`
				} `xml:"author"`
				BookTitle string `xml:"book-title"`
				Lang      string `xml:"lang"`
				Date      struct {
					Value string `xml:"value,attr"`
					Text  string `xml:",chardata"`
				} `xml:"date"`
				Sequences []struct {
					Name   string `xml:"name,attr"`
					Number string `xml:"number,attr"`
				} `xml:"sequence"`
			} `xml:"title-info"`
			PublishInfo struct {
				ISBN      string `xml:"isbn"`
				Publisher string `xml:"publisher"`
				Year      string `xml:"year"`
			} `xml:"publish-info"`
		} `xml:"description"`
	}
	decoder := xml.NewDecoder(reader)
	decoder.CharsetReader = ebookXMLCharsetReader
	if err := decoder.Decode(&fb2); err != nil {
		return book, err
	}

	book.Title = fb2.Description.TitleInfo.BookTitle
	book.Language = fb2.Description.TitleInfo.Lang
	book.Genres = fb2.Description.TitleInfo.Genres
	if t, ok := parseEbookDate(firstNonEmpty(fb2.Description.TitleInfo.Date.Value, fb2.Description.TitleInfo.Date.Text)); ok {
		book.PublishedAt = t
	}
	for _, author := range fb2.Description.TitleInfo.Authors {
		name := strings.Join(uniqueTrimmedStrings([]string{
			author.FirstName,
			author.MiddleName,
			author.LastName,
		}), " ")
		if name == "" {
			name = author.Nickname
		}
		book.Authors = append(book.Authors, name)
	}
	if len(fb2.Description.TitleInfo.Sequences) > 0 {
		book.Series = fb2.Description.TitleInfo.Sequences[0].Name
		book.SeriesIndex = fb2.Description.TitleInfo.Sequences[0].Number
	}
	book.ISBN = fb2.Description.PublishInfo.ISBN
	book.Publisher = fb2.Description.PublishInfo.Publisher
	if year, err := strconv.Atoi(strings.TrimSpace(fb2.Description.PublishInfo.Year)); err == nil {
		book.Year = year
	}
	return book, nil
}

func parsePDFInfoFields(data []byte) map[string]string {
	fields := map[string]string{}
	for _, key := range []string{"Title", "Author", "Subject", "Keywords", "CreationDate", "ISBN"} {
		if value, ok := findPDFInfoValue(data, key); ok {
			fields[key] = value
		}
	}
	return fields
}

// pdfWhitespace is the PDF whitespace character set (ISO 32000-1, table 1).
const pdfWhitespace = "\x00\t\n\f\r "

// isPDFTokenDelimiter reports whether b legally terminates a PDF name token.
// Without this check a key with a shared prefix (e.g. "/TitleSort") would be
// mistaken for the key itself (e.g. "/Title").
func isPDFTokenDelimiter(b byte) bool {
	switch b {
	case '\x00', '\t', '\n', '\f', '\r', ' ', '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	default:
		return false
	}
}

// findPDFInfoValue scans every occurrence of "/<key>" in the window and
// returns the first whose token is properly delimited and whose value parses
// as a PDF string. Raw byte search can match key-shaped noise inside
// compressed streams, so a failed parse moves on to the next occurrence
// instead of giving up.
func findPDFInfoValue(data []byte, key string) (string, bool) {
	token := []byte("/" + key)
	for offset := 0; offset < len(data); {
		idx := bytes.Index(data[offset:], token)
		if idx < 0 {
			return "", false
		}
		idx += offset
		offset = idx + len(token)
		rest := data[idx+len(token):]
		if len(rest) == 0 {
			return "", false
		}
		if !isPDFTokenDelimiter(rest[0]) {
			continue
		}
		trimmed := bytes.TrimLeft(rest, pdfWhitespace)
		if len(trimmed) == 0 {
			return "", false
		}
		if value, ok := readPDFString(trimmed); ok {
			return value, true
		}
	}
	return "", false
}

func decodeEbookXML(data []byte, v any) error {
	data = normalizeEbookXMLVersion(data)
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = ebookXMLCharsetReader
	return decoder.Decode(v)
}

func normalizeEbookXMLVersion(data []byte) []byte {
	for _, needle := range []string{`version="1.1"`, `version='1.1'`} {
		idx := bytes.Index(data, []byte(needle))
		if idx < 0 || idx > 128 {
			continue
		}
		out := append([]byte(nil), data...)
		copy(out[idx:], strings.Replace(needle, "1.1", "1.0", 1))
		return out
	}
	return data
}

func ebookXMLCharsetReader(label string, input io.Reader) (io.Reader, error) {
	name := strings.ToLower(strings.TrimSpace(label))
	if name == "" || name == "utf-8" || name == "utf8" {
		return input, nil
	}
	reader, err := charset.NewReaderLabel(name, input)
	if err != nil {
		return nil, fmt.Errorf("unsupported ebook XML encoding %q: %w", label, err)
	}
	return reader, nil
}

func readPDFString(data []byte) (string, bool) {
	switch data[0] {
	case '(':
		return readPDFLiteralString(data)
	case '<':
		if len(data) > 1 && data[1] == '<' {
			return "", false
		}
		return readPDFHexString(data)
	default:
		return "", false
	}
}

func readPDFLiteralString(data []byte) (string, bool) {
	if len(data) == 0 || data[0] != '(' {
		return "", false
	}
	var out []byte
	depth := 1
	escaped := false
	for _, b := range data[1:] {
		if escaped {
			switch b {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			default:
				out = append(out, b)
			}
			escaped = false
			continue
		}
		switch b {
		case '\\':
			escaped = true
		case '(':
			depth++
			out = append(out, b)
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimSpace(decodePDFLiteralBytes(out)), true
			}
			out = append(out, b)
		default:
			out = append(out, b)
		}
	}
	return "", false
}

func readPDFHexString(data []byte) (string, bool) {
	if len(data) == 0 || data[0] != '<' {
		return "", false
	}
	end := bytes.IndexByte(data[1:], '>')
	if end < 0 {
		return "", false
	}
	raw := data[1 : end+1]
	var cleaned []byte
	for _, b := range raw {
		switch {
		case b == ' ' || b == '\t' || b == '\r' || b == '\n':
			continue
		case (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F'):
			cleaned = append(cleaned, b)
		default:
			return "", false
		}
	}
	if len(cleaned)%2 == 1 {
		cleaned = append(cleaned, '0')
	}
	decoded := make([]byte, hex.DecodedLen(len(cleaned)))
	if _, err := hex.Decode(decoded, cleaned); err != nil {
		return "", false
	}
	return strings.TrimSpace(decodePDFLiteralBytes(decoded)), true
}

func decodePDFLiteralBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	switch {
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		if decoded, err := textunicode.UTF16(textunicode.BigEndian, textunicode.ExpectBOM).NewDecoder().Bytes(data); err == nil {
			return string(decoded)
		}
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		if decoded, err := textunicode.UTF16(textunicode.LittleEndian, textunicode.ExpectBOM).NewDecoder().Bytes(data); err == nil {
			return string(decoded)
		}
	}
	// The PDF spec says non-UTF-16 strings are PDFDocEncoding, but real-world
	// producers commonly emit UTF-8 (PDF 2.0 even allows a UTF-8 BOM). Only
	// fall back to the Windows-1252 approximation for non-UTF-8 bytes so
	// UTF-8 metadata is not mojibaked.
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if utf8.Valid(data) {
		return string(data)
	}
	if decoded, err := charmap.Windows1252.NewDecoder().Bytes(data); err == nil {
		return string(decoded)
	}
	return strings.ToValidUTF8(string(data), "")
}

func splitEbookAuthors(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == '|'
	})
	if len(parts) == 1 {
		parts = strings.Split(value, " and ")
	}
	return uniqueTrimmedStrings(parts)
}

func splitPDFKeywords(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return uniqueTrimmedStrings(strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	}))
}

func parsePDFDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "D:") {
		value = strings.TrimPrefix(value, "D:")
	}
	value = strings.TrimSuffix(value, "Z")
	if len(value) >= 14 {
		if t, err := time.Parse("20060102150405", value[:14]); err == nil && t.Year() > 0 {
			return t, true
		}
	}
	if len(value) >= 8 {
		if t, err := time.Parse("20060102", value[:8]); err == nil && t.Year() > 0 {
			return t, true
		}
	}
	if len(value) >= 4 {
		if t, err := time.Parse("2006", value[:4]); err == nil && t.Year() > 0 {
			return t, true
		}
	}
	return time.Time{}, false
}

func readEPUBZipEntry(reader *zip.Reader, name string) ([]byte, error) {
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer entry.Close()
		limited := io.LimitReader(entry, maxEPUBMetadataEntrySize+1)
		data, err := io.ReadAll(limited)
		if err != nil {
			return nil, err
		}
		if len(data) > maxEPUBMetadataEntrySize {
			return nil, fmt.Errorf("epub entry too large: %s", name)
		}
		return data, nil
	}
	return nil, fmt.Errorf("epub entry not found: %s", name)
}

func epubOPFPath(container []byte) (string, error) {
	var parsed struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := decodeEbookXML(container, &parsed); err != nil {
		return "", err
	}
	for _, rootfile := range parsed.Rootfiles {
		if strings.TrimSpace(rootfile.FullPath) != "" {
			return rootfile.FullPath, nil
		}
	}
	return "", fmt.Errorf("epub container has no rootfile")
}

func parseEPUBOPFMetadata(opf []byte, book *parsedEbook) error {
	var parsed struct {
		Metadata struct {
			Titles       []string `xml:"title"`
			Creators     []string `xml:"creator"`
			Identifiers  []string `xml:"identifier"`
			Publisher    string   `xml:"publisher"`
			Dates        []string `xml:"date"`
			Language     string   `xml:"language"`
			Subjects     []string `xml:"subject"`
			Descriptions []string `xml:"description"`
			Meta         []struct {
				Name     string `xml:"name,attr"`
				Property string `xml:"property,attr"`
				Content  string `xml:"content,attr"`
				Value    string `xml:",chardata"`
			} `xml:"meta"`
		} `xml:"metadata"`
	}
	if err := decodeEbookXML(opf, &parsed); err != nil {
		return err
	}
	book.Title = firstNonEmpty(parsed.Metadata.Titles...)
	book.Authors = append(book.Authors, parsed.Metadata.Creators...)
	book.Publisher = parsed.Metadata.Publisher
	book.Language = parsed.Metadata.Language
	book.Genres = append(book.Genres, parsed.Metadata.Subjects...)
	book.Description = firstNonEmpty(parsed.Metadata.Descriptions...)
	for _, identifier := range parsed.Metadata.Identifiers {
		if isbn := normalizeEbookISBN(identifier); isbn != "" {
			book.ISBN = isbn
			break
		}
	}
	for _, date := range parsed.Metadata.Dates {
		if t, ok := parseEbookDate(date); ok {
			book.PublishedAt = t
			book.Year = t.Year()
			break
		}
	}
	for _, meta := range parsed.Metadata.Meta {
		name := strings.ToLower(strings.TrimSpace(firstNonEmpty(meta.Name, meta.Property)))
		value := strings.TrimSpace(firstNonEmpty(meta.Content, meta.Value))
		switch name {
		case "calibre:series", "belongs-to-collection":
			book.Series = value
		case "calibre:series_index", "group-position":
			book.SeriesIndex = value
		case "calibre:isbn", "isbn", "schema:isbn":
			if book.ISBN == "" {
				book.ISBN = normalizeEbookISBN(value)
			}
		}
	}
	return nil
}

func extractEPUBCover(reader *zip.Reader, opfPath string, opf []byte) (*parsedEbookCover, error) {
	var parsed struct {
		Metadata struct {
			Meta []struct {
				Name    string `xml:"name,attr"`
				Content string `xml:"content,attr"`
			} `xml:"meta"`
		} `xml:"metadata"`
		Manifest struct {
			Items []struct {
				ID         string `xml:"id,attr"`
				Href       string `xml:"href,attr"`
				MediaType  string `xml:"media-type,attr"`
				Properties string `xml:"properties,attr"`
			} `xml:"item"`
		} `xml:"manifest"`
	}
	if err := decodeEbookXML(opf, &parsed); err != nil {
		return nil, err
	}

	coverID := ""
	for _, meta := range parsed.Metadata.Meta {
		if strings.EqualFold(strings.TrimSpace(meta.Name), "cover") {
			coverID = strings.TrimSpace(meta.Content)
			break
		}
	}

	// An EPUB3 properties="cover-image" item is authoritative; the EPUB2
	// <meta name="cover"> id frequently points at the XHTML cover *page*
	// rather than the image, so it ranks lower and non-image manifest items
	// are skipped entirely instead of shadowing a later real cover image.
	var coverHref string
	var coverType string
	coverRank := 0
	for _, item := range parsed.Manifest.Items {
		rank := 0
		for _, prop := range strings.Fields(strings.ToLower(item.Properties)) {
			if prop == "cover-image" {
				rank = 2
				break
			}
		}
		if rank == 0 && coverID != "" && strings.TrimSpace(item.ID) == coverID {
			rank = 1
		}
		if rank <= coverRank || !isEPUBImageManifestItem(item.MediaType, item.Href) {
			continue
		}
		coverHref = strings.TrimSpace(item.Href)
		coverType = strings.TrimSpace(item.MediaType)
		coverRank = rank
		if coverRank == 2 {
			break
		}
	}
	if coverHref == "" {
		return nil, fmt.Errorf("epub cover not referenced")
	}

	coverPath := resolveEPUBRelativePath(opfPath, coverHref)
	data, err := readEPUBZipEntry(reader, coverPath)
	if err != nil {
		return nil, err
	}
	if coverType == "" {
		coverType = ebookImageContentType(coverPath)
	}
	return &parsedEbookCover{ContentType: coverType, Bytes: data}, nil
}

func isEPUBImageManifestItem(mediaType, href string) bool {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if mt != "" {
		return strings.HasPrefix(mt, "image/")
	}
	return ebookImageContentType(href) != "application/octet-stream"
}

func resolveEPUBRelativePath(baseFile string, href string) string {
	cleanHref := strings.TrimSpace(href)
	if decoded, err := urlPathUnescape(cleanHref); err == nil {
		cleanHref = decoded
	}
	baseDir := path.Dir(strings.ReplaceAll(baseFile, "\\", "/"))
	if baseDir == "." || strings.HasPrefix(cleanHref, "/") {
		baseDir = ""
	}
	return strings.TrimPrefix(path.Clean(path.Join(baseDir, cleanHref)), "/")
}

func urlPathUnescape(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '%' || i+2 >= len(value) {
			out.WriteByte(value[i])
			continue
		}
		hi := fromHex(value[i+1])
		lo := fromHex(value[i+2])
		if hi < 0 || lo < 0 {
			out.WriteByte(value[i])
			continue
		}
		out.WriteByte(byte(hi<<4 | lo))
		i += 2
	}
	return out.String(), nil
}

func fromHex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	default:
		return -1
	}
}

func ebookImageContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".avif":
		return "image/avif"
	case ".bmp":
		return "image/bmp"
	default:
		return "application/octet-stream"
	}
}

func parseEbookDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
