package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowseDirectoryPageAcrossBatchesAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	for i := 299; i >= 0; i-- {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("folder-%03d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"alias": "folder-000", "broken": "missing", "file-link": "file"} {
		if err := os.Symlink(filepath.Join(dir, target), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	h := NewFilesystemHandler()
	after := ""
	var all []CatalogImportSource
	for {
		page, err := h.BrowseDirectoryPage(t.Context(), dir, "", after, 17)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) > 17 {
			t.Fatal("unbounded page")
		}
		if len(page.Entries) == 0 {
			break
		}
		for _, entry := range page.Entries {
			if entry.Key <= after {
				t.Fatalf("unordered/repeated key %s", entry.Key)
			}
			after = entry.Key
			all = append(all, entry)
		}
	}
	if len(all) != 301 || filepath.Base(all[0].Key) != "alias" || filepath.Base(all[300].Key) != "folder-299" {
		t.Fatalf("unexpected listing: %d entries", len(all))
	}
	page, err := h.BrowseDirectoryPage(t.Context(), dir, "FOLDER-29", "", 2)
	if err != nil || len(page.Entries) != 2 || filepath.Base(page.Entries[0].Key) != "folder-290" {
		t.Fatalf("prefix page: %#v, %v", page, err)
	}
}

func TestLocalCatalogSourcesPageIncludesOnlyRegularBundles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"z.json.gz", "a.JSON.GZ", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("bundle"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "directory.json.gz"), 0700); err != nil {
		t.Fatal(err)
	}
	h := &CatalogSeedHandler{localImportDir: dir}
	page, err := h.ListLocalCatalogImportSourcesPage(t.Context(), "", 1)
	if err != nil || len(page) != 1 || filepath.Base(page[0].Key) != "a.JSON.GZ" || page[0].SizeBytes != 6 || page[0].LastModified == nil {
		t.Fatalf("first page: %#v, %v", page, err)
	}
	page, err = h.ListLocalCatalogImportSourcesPage(t.Context(), page[0].Key, 2)
	if err != nil || len(page) != 1 || filepath.Base(page[0].Key) != "z.json.gz" {
		t.Fatalf("second page: %#v, %v", page, err)
	}
	h.localImportDir = filepath.Join(dir, "absent")
	page, err = h.ListLocalCatalogImportSourcesPage(t.Context(), "", 2)
	if err != nil || len(page) != 0 {
		t.Fatalf("missing directory: %#v, %v", page, err)
	}
}
