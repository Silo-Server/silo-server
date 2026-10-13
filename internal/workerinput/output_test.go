package workerinput

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateOutputResolvesExistingAncestors(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	ctx := WithContext(t.Context(), &Adapter{options: Options{LocalRoot: root}})
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"safe-link": inside, "escape-link": outside, "dangling-link": filepath.Join(outside, "missing")} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{root, filepath.Join(root, "new", "nested", "file.ass"), filepath.Join(root, "safe-link", "new", "file.ass")} {
		if err := ValidateOutput(ctx, path); err != nil {
			t.Fatalf("owned output %q: %v", path, err)
		}
	}
	for _, path := range []string{outside, filepath.Join(root, "..", "escape"), filepath.Join(root, "escape-link", "new", "file.ass"), filepath.Join(root, "dangling-link", "new", "file.ass"), "relative.ass"} {
		if err := ValidateOutput(ctx, path); err == nil {
			t.Fatalf("unowned output %q admitted", path)
		}
	}
	if err := ValidateOutput(WithContext(t.Context(), &Adapter{}), filepath.Join(root, "file.ass")); err == nil {
		t.Fatal("missing scratch root admitted output")
	}
	if err := ValidateOutput(context.Background(), "legacy-relative.ass"); err != nil {
		t.Fatalf("legacy output changed: %v", err)
	}
}
