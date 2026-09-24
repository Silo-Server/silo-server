package themesongs

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Delivery is how a theme reaches the client: its original bytes, or the
// progressive AAC conversion used when the client cannot decode the original.
type Delivery string

const (
	DeliveryOriginal  Delivery = "original"
	DeliveryConverted Delivery = "converted"
)

// ConvertedContentType is the media type of a converted theme: audio-only
// fragmented MP4 carrying AAC, the same output as audio-only video remux.
const ConvertedContentType = playback.AudioOnlyRemuxMIMEV3

const (
	codecAAC        = "aac"
	containerMP4    = "mp4"
	convertedBitMax = 192
)

// Format is one container and audio codec pair a client reports it can decode.
// An empty AudioCodec accepts any codec in the container.
type Format struct {
	Container  string
	AudioCodec string
}

// Conversion freezes the AAC output a converted theme is encoded to.
// SourceChannels is set only for a surround source, which selects the shared
// surround-to-stereo downmix recipe.
type Conversion struct {
	Channels       int
	BitrateKbps    int
	SourceChannels int
}

// NormalizeCodec maps an ffprobe codec name, or a client's codec label, onto
// the vocabulary negotiation compares: every PCM sample format is "pcm".
func NormalizeCodec(codec string) string {
	codec = strings.ToLower(strings.TrimSpace(codec))
	if strings.HasPrefix(codec, "pcm") {
		return "pcm"
	}
	return codec
}

// containerFamily treats the MP4 audio extensions as one container.
func containerFamily(container string) string {
	switch container = strings.ToLower(strings.TrimSpace(container)); container {
	case "m4a", "m4b", containerMP4:
		return containerMP4
	default:
		return container
	}
}

// Negotiate chooses how file reaches a client that decodes accepted. An empty
// list is a client that did not describe itself; it receives the original, as
// every client did before conversion existed. ok is false when the client can
// decode neither the original nor the AAC conversion.
func Negotiate(file File, accepted []Format) (Delivery, bool) {
	if len(accepted) == 0 {
		return DeliveryOriginal, true
	}
	codec := NormalizeCodec(file.AudioCodec)
	for _, format := range accepted {
		want := NormalizeCodec(format.AudioCodec)
		if containerFamily(format.Container) == containerFamily(file.Container) && (want == "" || want == codec) {
			return DeliveryOriginal, true
		}
	}
	for _, format := range accepted {
		want := NormalizeCodec(format.AudioCodec)
		if containerFamily(format.Container) == containerMP4 && (want == "" || want == codecAAC) {
			return DeliveryConverted, true
		}
	}
	return "", false
}

// ConversionFor is the AAC output for file: mono stays mono, everything else
// becomes stereo, at the encoder's default bitrate capped at 192 kbps.
func ConversionFor(file File) Conversion {
	target := 2
	if file.AudioChannels == 1 {
		target = 1
	}
	channels, bitrate := playback.ResolveAACOutputV3(target, 0)
	conversion := Conversion{Channels: channels, BitrateKbps: min(bitrate, convertedBitMax)}
	if file.AudioChannels > 2 {
		conversion.SourceChannels = file.AudioChannels
	}
	return conversion
}
