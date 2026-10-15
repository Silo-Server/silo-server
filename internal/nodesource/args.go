package nodesource

// AdmittedFormats are the demuxers FFmpeg may use on a relayed source: the
// containers the scanner catalogs as video, audio and theme files. Playlist,
// concat, image-sequence and other demuxers that open further URLs or local
// files are left out, so a crafted source cannot make FFmpeg read anything but
// its own bytes.
const AdmittedFormats = "matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,avi,mpegts,mpeg,mpegvideo,hevc,h264,flac,ogg,mp3,aac,wav,asf,flv"

// ProtocolWhitelist is the protocols FFmpeg may use on a relayed source: HTTP
// over TCP to the loopback relay, nothing else.
const ProtocolWhitelist = "http,tcp"

// The FFmpeg input options that restrict a relayed input.
const (
	protocolWhitelistOption = "-protocol_whitelist"
	formatWhitelistOption   = "-format_whitelist"
)

// inputRestrictions are the input options that hold FFmpeg or ffprobe to one
// relayed source.
var inputRestrictions = []string{protocolWhitelistOption, ProtocolWhitelist, formatWhitelistOption, AdmittedFormats}

// RestrictArgs returns FFmpeg's argv with every relayed input (a "-i" naming
// a loopback relay URL) held to the relay's protocols and the admitted
// demuxers, the options placed just before its "-i". It fails closed: a relay
// URL whose handle is released or unknown is restricted too, so a stale one
// can never reach FFmpeg with its default protocols. An argv with no relay
// URL input, such as any local path, is returned unchanged.
func RestrictArgs(args []string) []string {
	return restrict(args, false)
}

// RestrictProbeArgs is RestrictArgs for ffprobe, whose input is usually
// positional: a loopback relay URL anywhere in its argv, after "-i" or
// on its own, gets the options just before it.
func RestrictProbeArgs(args []string) []string {
	return restrict(args, true)
}

func restrict(args []string, positional bool) []string {
	var out []string
	copied := 0
	for i, arg := range args {
		if !IsRemote(arg) && !isLoopbackSourceURL(arg) {
			continue
		}
		at := i
		if i > 0 && args[i-1] == "-i" {
			at = i - 1
		} else if !positional {
			continue
		}
		if out == nil {
			out = make([]string, 0, len(args)+len(inputRestrictions))
		}
		out = append(append(out, args[copied:at]...), inputRestrictions...)
		copied = at
	}
	if out == nil {
		return args
	}
	return append(out, args[copied:]...)
}
