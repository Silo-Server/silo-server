package apiv2

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestEbookTextPages(t *testing.T) {
	path := ebookTextFixture(t)
	file := &models.MediaFile{ID: 42, ContentID: "book", BaseType: "ebook", Container: "epub", FilePath: path}
	access := &ebookFileAccess{}
	deps := pilotDeps(nil, nil)
	deps.EbookFiles = handlers.NewEbookReaderHandler(&handlers.MediaFileAuthorizer{FileResolver: ebookFileResolver{file: file}, ItemAccess: access})
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	url := Prefix + "/ebooks/book/files/42/text-pages/"
	capability := do(t, h, "GET", Prefix+"/capabilities/ebooks", "", viewer)
	if capability.Code != 200 || !strings.Contains(capability.Body.String(), `"text_pages":true`) {
		t.Fatal(capability.Code, capability.Body.String())
	}
	rec := do(t, h, "GET", url+"1", "", viewer)
	if rec.Code != 200 {
		t.Fatalf("page: %d %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Revision, Location, Text string
		PageIndex                int  `json:"page_index"`
		TotalPages               int  `json:"total_pages"`
		Previous                 *int `json:"previous_page"`
		Next                     *int `json:"next_page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Text != "界界" || page.PageIndex != 1 || page.TotalPages != 2 || page.Previous == nil || *page.Previous != 0 || page.Next != nil || page.Revision == "" || !strings.HasPrefix(page.Location, "silo-text-v1:") {
		t.Fatalf("page: %+v", page)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") || strings.Contains(rec.Body.String(), path) {
		t.Fatal("unsafe response")
	}
	if access.filter.ProfileID != "p-owner" || access.filter.UserID != 1 {
		t.Fatal(access.filter)
	}
	for _, headers := range []map[string]string{nil, bearer(memberToken), with(bearer(memberToken), "X-Profile-Id", "p-other")} {
		if r := do(t, h, "GET", url+"0", "", headers); r.Code == 200 {
			t.Fatal("unauthorized page")
		}
	}
	for _, target := range []string{url + "2", url + "-1", url + "abc", Prefix + "/ebooks/other/files/42/text-pages/0", Prefix + "/ebooks/book/files/0/text-pages/0"} {
		if r := do(t, h, "GET", target, "", viewer); r.Code < 400 {
			t.Fatal(target, r.Code)
		}
	}
	access.denied = true
	if r := do(t, h, "GET", url+"0", "", viewer); r.Code != 404 {
		t.Fatal(r.Code)
	}
	access.denied = false
	file.Container = "pdf"
	if r := do(t, h, "GET", url+"0", "", viewer); r.Code != 415 {
		t.Fatal(r.Code, r.Body.String())
	}
	file.Container = "epub"
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := do(t, h, "GET", url+"0", "", viewer); r.Code != 422 || strings.Contains(r.Body.String(), path) {
		t.Fatal(r.Code, r.Body.String())
	}
	if err := os.Truncate(path, (32<<20)+1); err != nil {
		t.Fatal(err)
	}
	if r := do(t, h, "GET", url+"0", "", viewer); r.Code != 413 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestEbookTextPageRejectsSpecialFilesWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.epub")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Unblock the old blocking-open behavior even when the regression fails.
	t.Cleanup(func() {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
		}
	})
	deps := pilotDeps(nil, nil)
	deps.EbookFiles = handlers.NewEbookReaderHandler(&handlers.MediaFileAuthorizer{FileResolver: ebookFileResolver{file: &models.MediaFile{ID: 42, ContentID: "book", BaseType: "ebook", Container: "epub", FilePath: path}}, ItemAccess: &ebookFileAccess{}})
	h := newTestHandler(t, deps)
	done := make(chan int, 1)
	go func() {
		rec := do(t, h, "GET", Prefix+"/ebooks/book/files/42/text-pages/0", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
		done <- rec.Code
	}()
	select {
	case status := <-done:
		if status != 503 {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("special-file open blocked extraction")
	}
}

func TestEbookTextPageDocumentsErrors(t *testing.T) {
	doc := generatedDocument(t)
	paths := doc["paths"].(map[string]any)
	op := paths[Prefix+"/ebooks/{content_id}/files/{file_id}/text-pages/{page_index}"].(map[string]any)["get"].(map[string]any)
	responses := op["responses"].(map[string]any)
	for _, status := range []string{"408", "413", "415", "503"} {
		if _, ok := responses[status]; !ok {
			t.Errorf("missing response %s", status)
		}
	}
}

func ebookTextFixture(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"book.opf":               `<package xmlns="http://www.idpf.org/2007/opf"><manifest><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="one"/></spine></package>`,
		"one.xhtml":              `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>` + strings.Repeat("界", 2050) + `</p></body></html>`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private.epub")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
