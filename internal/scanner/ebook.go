package scanner

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
	"golang.org/x/text/encoding"
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
	".md":   true,
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
	case ".mobi", ".azw", ".azw3", ".cbr", ".md":
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

	data, err := io.ReadAll(io.LimitReader(file, maxPDFMetadataScanSize))
	if err != nil {
		return book, err
	}
	info := parsePDFInfoFields(data)
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
	file, err := os.Open(path)
	if err != nil {
		return parsedEbook{Format: "fb2"}, err
	}
	defer file.Close()
	return parseEbookFB2Reader(file, "fb2")
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

	var pages []*zip.File
	for _, file := range reader.File {
		if isComicArchivePage(file.Name) {
			book.PageCount++
			pages = append(pages, file)
		}
	}
	sort.Slice(pages, func(i, j int) bool {
		return normalizedArchivePath(pages[i].Name) < normalizedArchivePath(pages[j].Name)
	})
	if len(pages) > 0 {
		if cover, err := readArchiveImageCover(pages[0]); err == nil {
			book.Cover = cover
		}
	}
	return book, nil
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
	if err := xml.NewDecoder(reader).Decode(&fb2); err != nil {
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
		token := []byte("/" + key)
		idx := bytes.Index(data, token)
		if idx < 0 {
			continue
		}
		rest := bytes.TrimLeft(data[idx+len(token):], " \t\r\n")
		if len(rest) == 0 || rest[0] != '(' {
			continue
		}
		value, ok := readPDFLiteralString(rest)
		if ok {
			fields[key] = value
		}
	}
	return fields
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

func ebookXMLCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	name := strings.ToLower(strings.TrimSpace(charset))
	var enc encoding.Encoding
	switch name {
	case "", "utf-8", "utf8":
		return input, nil
	case "iso-8859-1", "latin1", "latin-1":
		enc = charmap.ISO8859_1
	case "windows-1252", "cp1252":
		enc = charmap.Windows1252
	case "utf-16", "utf16":
		enc = textunicode.UTF16(textunicode.BigEndian, textunicode.ExpectBOM)
	case "utf-16be", "utf16be":
		enc = textunicode.UTF16(textunicode.BigEndian, textunicode.IgnoreBOM)
	case "utf-16le", "utf16le":
		enc = textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM)
	default:
		return nil, fmt.Errorf("unsupported ebook XML encoding %q", charset)
	}
	return enc.NewDecoder().Reader(input), nil
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
		if t, err := time.Parse("20060102150405", value[:14]); err == nil {
			return t, true
		}
	}
	if len(value) >= 8 {
		if t, err := time.Parse("20060102", value[:8]); err == nil {
			return t, true
		}
	}
	if len(value) >= 4 {
		if t, err := time.Parse("2006", value[:4]); err == nil {
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

	var coverHref string
	var coverType string
	for _, item := range parsed.Manifest.Items {
		id := strings.TrimSpace(item.ID)
		props := strings.Fields(strings.ToLower(item.Properties))
		isCover := coverID != "" && id == coverID
		for _, prop := range props {
			if prop == "cover-image" {
				isCover = true
				break
			}
		}
		if !isCover {
			continue
		}
		coverHref = strings.TrimSpace(item.Href)
		coverType = strings.TrimSpace(item.MediaType)
		break
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
