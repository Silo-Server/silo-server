package scanner

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ebookExtensions = map[string]bool{
	".epub": true,
	".pdf":  true,
	".mobi": true,
	".azw":  true,
	".azw3": true,
	".fb2":  true,
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
	return ebookExtensions[strings.ToLower(filepath.Ext(filePath))]
}

func parseEbookFile(path string) (book parsedEbook, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic parsing ebook %s: %v", path, r)
		}
	}()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".epub":
		book, err = parseEbookEPUB(path)
	case ".fb2":
		book, err = parseEbookFB2(path)
	case ".pdf", ".mobi", ".azw", ".azw3":
		book = parsedEbook{Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")}
	default:
		err = fmt.Errorf("unsupported ebook format: %s", filepath.Ext(path))
	}
	book.sanitize()
	return book, err
}

func (b *parsedEbook) sanitize() {
	b.Format = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(b.Format)), ".")
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
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
	value = strings.TrimPrefix(value, "ISBN:")
	value = strings.TrimPrefix(value, "ISBN")
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
	return book, nil
}

func parseEbookFB2(path string) (parsedEbook, error) {
	book := parsedEbook{Format: "fb2"}
	file, err := os.Open(path)
	if err != nil {
		return book, err
	}
	defer file.Close()

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
	if err := xml.NewDecoder(file).Decode(&fb2); err != nil {
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
		return io.ReadAll(entry)
	}
	return nil, fmt.Errorf("epub entry not found: %s", name)
}

func epubOPFPath(container []byte) (string, error) {
	var parsed struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(container, &parsed); err != nil {
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
	if err := xml.Unmarshal(opf, &parsed); err != nil {
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
		}
	}
	return nil
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
