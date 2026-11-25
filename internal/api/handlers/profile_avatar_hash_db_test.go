package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestProfileAvatarSourceHashDB: a profile's avatar hash names the picture it
// shows. It catches a hash that names another image: one kept after the
// picture changed, the wrong bytes hashed, or an old upload hashed again
// after its stored file changed.
func TestProfileAvatarSourceHashDB(t *testing.T) {
	f := newProfileLimitDBFixture(t, 5)
	ctx := t.Context()
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.handler.AvatarStore = blobs
	picture := testPNG(t)
	upload := func(source string) {
		t.Helper()
		if _, err := f.handler.UploadAvatar(ctx, ProfileAvatarUpload{
			UserID: f.userID, ProfileID: f.primary, ContentType: "image/png",
			File: bytes.NewReader(picture), SourceSHA256: source,
		}); err != nil {
			t.Fatalf("upload: %v", err)
		}
	}
	stored := func() userstore.Profile {
		t.Helper()
		p, err := f.store.GetProfile(ctx, f.primary)
		if err != nil || p == nil {
			t.Fatalf("profile = %v, err = %v", p, err)
		}
		return *p
	}
	sha := func(data []byte) string {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}

	given := sha([]byte("the file as another server first received it"))
	upload(given)
	if got := stored().AvatarSourceSHA256; got != given {
		t.Fatalf("hash with source_sha256 = %q, want %q", got, given)
	}
	upload("")
	if got := stored().AvatarSourceSHA256; got != sha(picture) {
		t.Fatalf("hash without source_sha256 = %q, want the received bytes' %q", got, sha(picture))
	}
	preset := "preset:avatar-3"
	if err := f.store.UpdateProfile(ctx, f.primary, userstore.UpdateProfileInput{Avatar: &preset}); err != nil {
		t.Fatal(err)
	}
	if got := stored().AvatarSourceSHA256; got != "" {
		t.Fatalf("hash after choosing a preset = %q, want none", got)
	}
	upload("")
	if _, err := f.handler.DeleteAvatar(ctx, f.userID, f.primary); err != nil {
		t.Fatal(err)
	}
	if got := stored().AvatarSourceSHA256; got != "" {
		t.Fatalf("hash after deleting the avatar = %q, want none", got)
	}

	// An upload from before hashes were kept: the stored original's hash,
	// computed once and kept.
	upload("")
	if _, err := f.pool.Exec(ctx, `UPDATE user_profiles SET avatar_source_sha256 = NULL WHERE user_id = $1 AND id = $2`, f.userID, f.primary); err != nil {
		t.Fatal(err)
	}
	original, err := blobstore.GetBytes(ctx, blobs, uploadedAvatarOriginalKey(f.userID, f.primary))
	if err != nil {
		t.Fatal(err)
	}
	legacy := stored()
	got, err := f.handler.profileAvatarSourceSHA256(ctx, f.store, legacy)
	if err != nil || got != sha(original) {
		t.Fatalf("old upload hash = %q, %v; want the stored original's %q", got, err, sha(original))
	}
	if kept := stored().AvatarSourceSHA256; kept != got {
		t.Fatalf("kept hash = %q, want %q", kept, got)
	}
	if err := blobs.Put(ctx, uploadedAvatarOriginalKey(f.userID, f.primary), []byte("rewritten")); err != nil {
		t.Fatal(err)
	}
	if again, err := f.handler.profileAvatarSourceSHA256(ctx, f.store, legacy); err != nil || again != got {
		t.Fatalf("second ask = %q, %v; want the kept %q", again, err, got)
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
