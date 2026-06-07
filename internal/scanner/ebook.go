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

func (b *parsedEbook) sanitize() {
	b.Format = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(b.Format)), ".")
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Publisher = strings.TrimSpace(b.Publisher)
	b.Language = strings.TrimSpace(b.Language)
	b.ISBN = normalizeEbookExternalID(b.ISBN)
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

func normalizeEbookExternalID(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", ""), " ", ""))
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
	case ".pdf":
		book, err = parseEbookPDF(path)
	case ".mobi", ".azw", ".azw3":
		book, err = parseEbookMOBI(path)
	case ".fb2":
		book, err = parseEbookFB2(path)
	default:
		err = fmt.Errorf("unsupported ebook format: %s", filepath.Ext(path))
	}
	book.sanitize()
	return book, err
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

func parseEbookPDF(path string) (parsedEbook, error) {
	return parsedEbook{Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")}, nil
}

func parseEbookMOBI(path string) (parsedEbook, error) {
	return parsedEbook{Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")}, nil
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
	for _, author := range fb2.Description.TitleInfo.Authors {
		name := strings.TrimSpace(strings.Join([]string{
			author.FirstName,
			author.MiddleName,
			author.LastName,
		}, " "))
		if name == "" {
			name = author.Nickname
		}
		book.Authors = append(book.Authors, name)
	}
	if len(fb2.Description.TitleInfo.Sequences) > 0 {
		book.Series = fb2.Description.TitleInfo.Sequences[0].Name
		book.SeriesIndex = fb2.Description.TitleInfo.Sequences[0].Number
	}
	book.ISBN = extractEbookISBN(fb2.Description.PublishInfo.ISBN)
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
	decoder := xml.NewDecoder(strings.NewReader(string(opf)))
	inMetadata := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Local == "metadata" {
				inMetadata = true
				continue
			}
			if !inMetadata {
				continue
			}
			if token.Name.Local == "meta" {
				readEPUBMetaElement(token, book)
				continue
			}
			var value string
			if err := decoder.DecodeElement(&value, &token); err != nil {
				return err
			}
			applyEPUBMetadataValue(token.Name.Local, value, book)
		case xml.EndElement:
			if token.Name.Local == "metadata" {
				inMetadata = false
			}
		}
	}
}

func readEPUBMetaElement(element xml.StartElement, book *parsedEbook) {
	var name, content string
	for _, attr := range element.Attr {
		switch attr.Name.Local {
		case "name":
			name = attr.Value
		case "content":
			content = attr.Value
		}
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "calibre:series":
		book.Series = content
	case "calibre:series_index":
		book.SeriesIndex = content
	}
}

func applyEPUBMetadataValue(name string, value string, book *parsedEbook) {
	switch name {
	case "title":
		book.Title = value
	case "creator":
		book.Authors = append(book.Authors, value)
	case "language":
		book.Language = value
	case "identifier":
		if book.ISBN == "" {
			book.ISBN = extractEbookISBN(value)
		}
	case "publisher":
		book.Publisher = value
	case "description":
		book.Description = value
	case "date":
		book.PublishedAt = parseEbookDate(value)
	}
}

func parseEbookDate(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01", "2006"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func extractEbookISBN(value string) string {
	normalized := normalizeEbookExternalID(value)
	var out strings.Builder
	for _, r := range normalized {
		if (r >= '0' && r <= '9') || r == 'X' {
			out.WriteRune(r)
		}
	}
	isbn := out.String()
	if len(isbn) == 10 || len(isbn) == 13 {
		return isbn
	}
	return ""
}
