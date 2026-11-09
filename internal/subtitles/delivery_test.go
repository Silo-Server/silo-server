package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

const deliverySRT = "1\r\n00:00:01,000 --> 00:00:02,500\r\nHello\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nWorld\r\n"

func TestDeliveryBytesUnsupportedFormatServesStoredBytes(t *testing.T) {
	data := []byte("{1}{2}microdvd")
	got, err := DeliveryBytes(&DownloadedSubtitle{ID: 8, Format: FormatSUB, Timing: Timing{OffsetMS: 100}}, data)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("unsupported format: %q %v", got, err)
	}
}

// Every subtitle response is labeled UTF-8, so text in a legacy code page
// must be delivered decoded. Input that cannot be decoded safely is delivered
// as stored.
func TestDeliveryBytesDeliversUTF8(t *testing.T) {
	const (
		ruSRT = "1\r\n00:00:01,000 --> 00:00:02,000\r\nПривет, мир! Ёжик в тумане.\r\n"
		plSRT = "1\r\n00:00:01,000 --> 00:00:02,000\r\nZażółć gęślą jaźń. Śnieg, źdźbło.\r\n"
		frSRT = "1\r\n00:00:01,000 --> 00:00:02,000\r\nÇa va? Déjà vu, mon garçon. Il fait très beau aujourd'hui.\r\n"
		ruASS = "[Script Info]\r\nScriptType: v4.00+\r\n\r\n[Events]\r\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n" +
			"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\\an8\\i1}Привет{\\i0}, мир\r\n" //nolint:misspell // ASS syntax
	)
	encode := func(enc encoding.Encoding, text string) []byte {
		out, err := enc.NewEncoder().Bytes([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	const ruSigns = "1\r\n00:00:01,000 --> 00:00:02,000\r\nПОЛИЦИЯ. ПРОДАЕТСЯ ДОМ\r\n"
	cue := func(text string) string { return "1\r\n00:00:01,000 --> 00:00:02,000\r\n" + text + "\r\n" }
	assWith := func(text string) string {
		return "[Script Info]\r\nScriptType: v4.00+\r\nTitle: Sample\r\n\r\n[Events]\r\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n" +
			"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,," + text + "\r\n" //nolint:misspell // ASS syntax
	}
	untaggedRuASS := encode(charmap.Windows1251, assWith("Привет, мир!"))
	vobsub := []byte{0x00, 0x00, 0x01, 0xBA, 0x44, 0x00, 0x04, 0x00, 0x04, 0x01}
	cp1251Untagged := encode(charmap.Windows1251, ruSRT)
	damagedUTF8 := append([]byte(ruSRT), 0xC0, '\n')
	gbkUntagged := encode(simplifiedchinese.GBK, "1\r\n00:00:01,000 --> 00:00:02,000\r\n你好，世界\r\n")
	tests := []struct {
		name     string
		language string
		format   SubtitleFormat
		data     []byte
		want     string
	}{
		{"windows-1251 russian", "ru", FormatSRT, encode(charmap.Windows1251, ruSRT), ruSRT},
		{"koi8-r russian", "rus", FormatSRT, encode(charmap.KOI8R, ruSRT), ruSRT},
		{"windows-1251 capitals", "ru", FormatSRT, encode(charmap.Windows1251, ruSigns), ruSigns},
		{"short windows-1251 track", "ru", FormatSRT, encode(charmap.Windows1251, cue("Я люблю тебя")), cue("Я люблю тебя")},
		{"short windows-1251 capitals", "ru", FormatSRT, encode(charmap.Windows1251, cue("ПУТЬ")), cue("ПУТЬ")},
		{"cyrillic serbian ass", "sr-Cyrl", FormatASS, encode(charmap.Windows1251, assWith("Добро вече")), assWith("Добро вече")},
		{"windows-1250 polish", "pl", FormatSRT, encode(charmap.Windows1250, plSRT), plSRT},
		{"iso-8859-2 polish", "pol", FormatSRT, encode(charmap.ISO8859_2, plSRT), plSRT},
		{"iso-8859-2 czech", "cs", FormatSRT, encode(charmap.ISO8859_2, cue("Šel jsem domů. Karel je šťastný.")), cue("Šel jsem domů. Karel je šťastný.")},
		{"windows-1250 slovak", "sk", FormatSRT, encode(charmap.Windows1250, cue("Ľudia sú tu.")), cue("Ľudia sú tu.")},
		{"windows-1252 untagged", "", FormatSRT, encode(charmap.Windows1252, frSRT), frSRT},
		{"utf-16le with bom", "", FormatSRT, encode(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM), ruSRT), ruSRT},
		{"utf-16be with bom", "", FormatVTT, encode(unicode.UTF16(unicode.BigEndian, unicode.UseBOM), ruSRT), ruSRT},
		{"utf-16le chinese with bom", "zh", FormatSRT, encode(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM), cue("背上背包。背上装备。")), cue("背上背包。背上装备。")},
		{"windows-1251 ass keeps styling", "ru", FormatASS, encode(charmap.Windows1251, ruASS), ruASS},
		{"utf-8", "ru", FormatSRT, []byte(ruSRT), ruSRT},
		{"utf-8 with bom", "ru", FormatSRT, []byte("\ufeff" + ruSRT), "\ufeff" + ruSRT},
		{"untagged non-latin stays as stored", "", FormatSRT, cp1251Untagged, string(cp1251Untagged)},
		{"untagged non-latin ass stays as stored", "", FormatASS, untaggedRuASS, string(untaggedRuASS)},
		{"damaged utf-8 stays as stored", "ru", FormatSRT, damagedUTF8, string(damagedUTF8)},
		{"chinese without a script stays as stored", "zh", FormatSRT, gbkUntagged, string(gbkUntagged)},
		{"binary sub stays as stored", "ru", FormatSUB, vobsub, string(vobsub)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeliveryBytes(&DownloadedSubtitle{ID: 1, Language: tt.language, Format: tt.format}, tt.data)
			if err != nil || string(got) != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	// The timing correction applies to the decoded text.
	timed, err := DeliveryBytes(&DownloadedSubtitle{ID: 2, Language: "ru", Format: FormatSRT, Timing: Timing{OffsetMS: 1000}}, encode(charmap.Windows1251, ruSRT))
	if err != nil || !bytes.Contains(timed, []byte("00:00:02,000 --> 00:00:03,000\r\nПривет")) {
		t.Fatalf("timed: %q %v", timed, err)
	}
}

func TestManagerGetDeliveryContent(t *testing.T) {
	blobs := newMockBlobStore()
	blobs.keys["k"] = []byte(deliverySRT)
	manager := NewManager(newMockSubtitleRepo(), blobs)
	got, err := manager.GetDeliveryContent(context.Background(), &DownloadedSubtitle{S3Key: "k", Format: FormatSRT, Timing: Timing{OffsetMS: -1000}})
	if err != nil {
		t.Fatal(err)
	}
	cues, err := ParseCues(got)
	if err != nil || len(cues) != 2 || cues[0].Start != 0 || cues[1].Start != 2*time.Second {
		t.Fatalf("delivered cues: %+v %v", cues, err)
	}
	if !bytes.Equal(blobs.keys["k"], []byte(deliverySRT)) {
		t.Fatal("delivery modified the stored bytes")
	}
}

// The frozen v1 API serializes DownloadedSubtitle directly; timing must not
// appear in it.
func TestDownloadedSubtitleV1JSONShapeUnchanged(t *testing.T) {
	sub := DownloadedSubtitle{
		ID: 1, MediaFileID: 2, Provider: "upload", Language: "en", Format: FormatSRT, ReleaseName: "r",
		Score: 1.5, HearingImpaired: true, Revision: 9, S3Key: "secret", ContentSHA256: "abc",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Timing: Timing{OffsetMS: 1200, Scale: 1.001},
	}
	encoded, err := json.Marshal(sub)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":1,"media_file_id":2,"provider":"upload","language":"en","format":"srt","release_name":"r","score":1.5,"hearing_impaired":true,"created_at":"2026-01-02T03:04:05Z"}`
	if string(encoded) != want {
		t.Fatalf("v1 JSON changed:\n%s\nwant\n%s", encoded, want)
	}
}

func TestManagerTimingPatch(t *testing.T) {
	repo := newMockSubtitleRepo()
	manager := NewManager(repo, newMockBlobStore())
	ctx := context.Background()
	srt, err := manager.Upload(ctx, UploadRequest{MediaFileID: 1, Language: "en", Filename: "a.srt", Data: []byte(deliverySRT)})
	if err != nil {
		t.Fatal(err)
	}

	revision := srt.Revision
	updated, err := manager.UpdateDownloadedSubtitleWithRevision(ctx, srt.ID, SubtitleMetadataPatch{Timing: &Timing{OffsetMS: 750}}, new(revision))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Timing != (Timing{OffsetMS: 750, Scale: 1}) || updated.Revision != revision+1 {
		t.Fatalf("timing patch stored %+v revision %d", updated.Timing, updated.Revision)
	}

	if _, err := manager.UpdateDownloadedSubtitle(ctx, srt.ID, SubtitleMetadataPatch{Timing: &Timing{Scale: 2}}); !errors.Is(err, ErrInvalidTiming) {
		t.Fatalf("out-of-range scale: %v", err)
	}
	if _, err := manager.UpdateDownloadedSubtitle(ctx, srt.ID, SubtitleMetadataPatch{Timing: &Timing{OffsetMS: 700000}}); !errors.Is(err, ErrInvalidTiming) {
		t.Fatalf("out-of-range offset: %v", err)
	}

	sub, err := manager.StoreSubtitle(ctx, StoreSubtitleRequest{MediaFileID: 1, Provider: "p", Language: "en", Format: FormatSUB, Data: []byte("{1}{2}x")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateDownloadedSubtitle(ctx, sub.ID, SubtitleMetadataPatch{Timing: &Timing{OffsetMS: 100}}); !errors.Is(err, ErrTimingUnsupported) {
		t.Fatalf("unsupported format: %v", err)
	}
	current, _ := repo.GetDownloadedSubtitle(ctx, srt.ID)
	if current.Timing != updated.Timing {
		t.Fatalf("rejected patch changed timing: %+v", current.Timing)
	}
}
