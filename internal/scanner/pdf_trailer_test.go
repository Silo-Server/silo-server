package scanner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var pdfFixtureObject = regexp.MustCompile(`(?m)^(\d+) 0 obj\b`)

// completePDFMetadataFixture adds an xref section to the small object fragments
// used by the metadata decoding tests. Production detection follows startxref.
func completePDFMetadataFixture(data []byte) []byte {
	data = bytes.TrimSuffix(bytes.TrimSpace(data), []byte("%%EOF"))
	dictionary := []byte("<< >>")
	if idx := bytes.LastIndex(data, []byte("\ntrailer\n")); idx >= 0 {
		dictionary = bytes.Clone(bytes.TrimSpace(data[idx+len("\ntrailer\n"):]))
		data = data[:idx+1]
	}
	offsets := map[int]int{}
	maxObject := 0
	for _, match := range pdfFixtureObject.FindAllSubmatchIndex(data, -1) {
		n, _ := strconv.Atoi(string(data[match[2]:match[3]]))
		offsets[n] = match[0]
		maxObject = max(maxObject, n)
	}
	xref := len(data)
	data = fmt.Appendf(data, "xref\n0 %d\n0000000000 65535 f \n", maxObject+1)
	for n := 1; n <= maxObject; n++ {
		if offset, exists := offsets[n]; exists {
			data = fmt.Appendf(data, "%010d 00000 n \n", offset)
		} else {
			data = append(data, "0000000000 00000 f \n"...)
		}
	}
	data = fmt.Appendf(data, "trailer\n<< /Size %d %s\nstartxref\n%d\n%%%%EOF\n", maxObject+1, bytes.TrimPrefix(dictionary, []byte("<<")), xref)
	return data
}

func TestParseEbookPDFEncryptionReferencesInContent(t *testing.T) {
	for name, content := range map[string]string{
		"page text":    "BT (/Encrypt 9 0 R) Tj ET",
		"fake trailer": "trailer << /Encrypt 9 0 R >>\nstartxref\n12\n%%EOF",
		"fake xref":    "9 0 obj << /Type /XRef /Encrypt 10 0 R >> stream\nnoise",
	} {
		t.Run(name, func(t *testing.T) {
			data := fmt.Sprintf("%%PDF-1.7\n1 0 obj\n<< /Title (Real Title) /Author (Ada Writer) /Subject (Discusses /Encrypt 9 0 R.) >>\nendobj\n2 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)
			path := filepath.Join(t.TempDir(), "book.pdf")
			if err := os.WriteFile(path, completePDFMetadataFixture([]byte(data)), 0o644); err != nil {
				t.Fatal(err)
			}
			book, err := parseEbookFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if book.Title != "Real Title" || strings.Join(book.Authors, ",") != "Ada Writer" || book.Description != "Discusses /Encrypt 9 0 R." {
				t.Fatalf("metadata lost: %+v", book)
			}
		})
	}
}

func pdfTrailerFixture(dictionary, kind string) []byte {
	data := []byte("%PDF-1.7\n1 0 obj\n<< /Title (Real Title) >>\nendobj\n")
	offset := len(data)
	if kind == "table" {
		data = append(data, "xref\n0 1\n0000000000 65535 f \ntrailer\n"...)
		data = append(data, dictionary...)
	} else {
		data = fmt.Appendf(data, "2 0 obj\n%s\nstream\n", dictionary)
		// The dictionary remains readable even when the xref stream has filters.
		data = append(data, "binary /Encrypt 9 0 R\nendstream\nendobj"...)
	}
	return fmt.Appendf(data, "\nstartxref\n%d\n%%%%EOF\n", offset)
}

func TestPDFDocumentEncrypted(t *testing.T) {
	for _, kind := range []string{"table", "stream"} {
		for _, tc := range []struct {
			name      string
			entries   string
			encrypted bool
		}{
			{"absent", "", false},
			{"reference", "/Encrypt 9 0 R", true},
			{"direct dictionary", "/Encrypt << /Filter /Standard /V 1 >>", true},
			{"null", "/Encrypt null", false},
			{"escaped name", "/Encr#79pt 9 0 R", true},
			{"comments between reference tokens", "/Encrypt % comment\n9 % comment\r0 % comment\r\nR", true},
			{"comment", "% /Encrypt 9 0 R\n", false},
			{"literal string", "/Note (text \\( /Encrypt 9 0 R \\) with (nesting))", false},
			{"nested dictionary", "/Custom << /Encrypt 9 0 R >>", false},
			{"array", "/Custom [ /Encrypt 9 0 R (text) << /Encrypt 7 0 R >> ]", false},
			{"hex string", "/ID [<2f456e6372797074203920302052><00>]", false},
			{"prefix name", "/EncryptMetadata false", false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				data := pdfTrailerFixture("<< /Size 3 /Type /XRef /W [1 2 1] /Filter /FlateDecode "+tc.entries+" >>", kind)
				got, err := pdfDocumentEncrypted(bytes.NewReader(data), int64(len(data)))
				if err != nil || got != tc.encrypted {
					t.Fatalf("encrypted = %v, err = %v; want %v", got, err, tc.encrypted)
				}
			})
		}
	}
}

func TestPDFDocumentEncryptedFollowsLinkedSections(t *testing.T) {
	for _, link := range []string{"Prev", "XRefStm"} {
		for _, encrypted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/encrypted=%t", link, encrypted), func(t *testing.T) {
				data := []byte("%PDF-1.7\n")
				old := len(data)
				entries := ""
				if encrypted {
					entries = "/Encrypt 9 0 R"
				}
				data = fmt.Appendf(data, "9 0 obj\n<< /Type /XRef /Size 10 %s >>\nstream\n\nendstream\nendobj\n", entries)
				latest := len(data)
				data = fmt.Appendf(data, "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 10 /%s %d >>\nstartxref\n%d\n%%%%EOF\n", link, old, latest)
				got, err := pdfDocumentEncrypted(bytes.NewReader(data), int64(len(data)))
				if err != nil || got != encrypted {
					t.Fatalf("encrypted = %v, err = %v; want %v", got, err, encrypted)
				}
			})
		}
	}
}

func TestPDFDocumentEncryptedLinearized(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		data := []byte("%PDF-1.7\n1 0 obj\n<< /Linearized 1 >>\nendobj\n")
		first := len(data)
		entries := ""
		if encrypted {
			entries = "/Encrypt 9 0 R"
		}
		// EOF points to the first-page xref. Its Prev points forward to the
		// main section at the end, whose trailer contains only Size.
		data = fmt.Appendf(data, "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 10 %s /Prev 0000000000 >>\n", entries)
		data = append(data, bytes.Repeat([]byte("x"), maxPDFMetadataScanSize+100)...)
		last := len(data)
		data = bytes.Replace(data, []byte("/Prev 0000000000"), fmt.Appendf(nil, "/Prev %010d", last), 1)
		data = fmt.Appendf(data, "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 10 >>\nstartxref\n%d\n%%%%EOF\n", first)
		got, err := pdfDocumentEncrypted(bytes.NewReader(data), int64(len(data)))
		if err != nil || got != encrypted {
			t.Fatalf("encrypted = %v, err = %v; want %v", got, err, encrypted)
		}
	}
}

func TestPDFDocumentEncryptedSharedAndCyclicSections(t *testing.T) {
	for _, cyclic := range []bool{false, true} {
		data := []byte("%PDF-1.7\n")
		first := len(data)
		entries := ""
		if cyclic {
			entries = fmt.Sprintf("/Prev %d", first)
		}
		data = fmt.Appendf(data, "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 3 %s >>\n", entries)
		stream := len(data)
		data = fmt.Appendf(data, "2 0 obj\n<< /Type /XRef /Size 3 /Prev %d >>\nstream\n\nendstream\nendobj\n", first)
		last := len(data)
		data = fmt.Appendf(data, "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 3 /Prev %d /XRefStm %d >>\nstartxref\n%d\n%%%%EOF\n", first, stream, last)
		got, err := pdfDocumentEncrypted(bytes.NewReader(data), int64(len(data)))
		if got || (err != nil) != cyclic {
			t.Fatalf("cyclic=%t: encrypted=%t, err=%v", cyclic, got, err)
		}
	}
}

func TestPDFDocumentEncryptedLargeXRef(t *testing.T) {
	const count = maxPDFMetadataScanSize/20 + 1
	for _, eol := range []string{"\n", "\r", "\r\n"} {
		data := []byte("%PDF-1.7\n")
		offset := len(data)
		data = fmt.Appendf(data, "xref%s0 %d%s", eol, count, eol)
		entry := "0000000000 65535 f"
		if len(eol) == 1 {
			entry += " "
		}
		data = append(data, bytes.Repeat([]byte(entry+eol), count)...)
		data = fmt.Appendf(data, "trailer\n<< /Size %d /Encrypt 9 0 R >>\nstartxref\n%d\n%%%%EOF\n", count, offset)
		got, err := pdfDocumentEncrypted(bytes.NewReader(data), int64(len(data)))
		if err != nil || !got {
			t.Fatalf("EOL %q: encrypted = %v, err = %v", eol, got, err)
		}
	}
}

func TestPDFDocumentEncryptedRejectsExcessiveHistory(t *testing.T) {
	data := []byte("%PDF-1.7\n")
	previous := 0
	for range 65 {
		entries := ""
		if previous > 0 {
			entries = fmt.Sprintf("/Prev %d", previous)
		}
		previous = len(data)
		data = fmt.Appendf(data, "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 1 %s >>\n", entries)
	}
	data = fmt.Appendf(data, "startxref\n%d\n%%%%EOF\n", previous)
	if _, err := pdfDocumentEncrypted(bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("excessive cross-reference history accepted")
	}
}

func TestPDFDocumentEncryptedRejectsMalformedStructure(t *testing.T) {
	for _, entries := range []string{
		"/Encrypt 9 0 R2", "/Encrypt /null", "/Encrypt (null)", "/Encrypt false",
		"/Prev -1", "/Prev 9999999999999999999999999999", "/Prev 9 0 R", "/XRefStm 999999999",
		"/Encrypt 9 0 R /Encrypt null", "/Note (unterminated", "/Custom [1 2", "/Custom <01zz>",
		"/Custom " + strings.Repeat("[", 65) + strings.Repeat("]", 65),
	} {
		t.Run(entries, func(t *testing.T) {
			data := pdfTrailerFixture("<< /Size 3 "+entries+" >>", "table")
			if _, err := pdfDocumentEncrypted(bytes.NewReader(data), int64(len(data))); err == nil {
				t.Fatal("malformed trailer accepted")
			}
		})
	}
	for _, data := range [][]byte{
		nil,
		[]byte("%PDF-1.7\n/Encrypt 9 0 R"),
		[]byte("%PDF-1.7\nstartxref\n999999\n%%EOF"),
		pdfTrailerFixture("<< /Type /Page /Encrypt 9 0 R >>", "stream"),
		[]byte("%PDF-1.7\nxref\n0 999999999999999999\nstartxref\n9\n%%EOF"),
	} {
		if _, err := pdfDocumentEncrypted(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Fatalf("malformed PDF accepted: %.100q", data)
		}
	}
}

func TestParseEbookPDFUnknownEncryptionKeepsSidecarFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n1 0 obj << /Title (Untrusted bytes) >> endobj\n%%EOF"), 0o644); err != nil {
		t.Fatal(err)
	}
	book, err := parseEbookFile(path)
	if err != nil || book.Title != "" || book.Format != "pdf" {
		t.Fatalf("book = %+v, err = %v", book, err)
	}
	opf := "<package><metadata><title>Sidecar Title</title><creator>Ada Writer</creator></metadata></package>"
	if err := os.WriteFile(strings.TrimSuffix(path, ".pdf")+".opf", []byte(opf), 0o644); err != nil {
		t.Fatal(err)
	}
	book, err = parseEbookFile(path)
	if err != nil || book.Title != "Sidecar Title" || strings.Join(book.Authors, ",") != "Ada Writer" {
		t.Fatalf("sidecar metadata lost: %+v, err = %v", book, err)
	}
}
