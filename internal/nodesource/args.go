package nodesource

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/mediacontainer"
)

// The FFmpeg argument guard. FFmpeg opens an http input with whatever
// protocols and demuxers its build has, and some demuxers (playlists,
// concat, image sequences, HLS, DASH) open further URLs or local files named
// inside the bytes they read. A relayed input is held to the relay's
// protocols and to the demuxers of the containers Silo catalogs, so a crafted
// source cannot make FFmpeg read anything but its own bytes, and it bypasses
// any HTTP proxy so the relay URL never leaves the host. internal/fspath
// applies the guard to every launch (fspath.CommandContext).

// ProtocolWhitelist is the protocols FFmpeg may use on a relayed input: HTTP
// over TCP to the loopback relay, nothing else.
const ProtocolWhitelist = "http,tcp"

// AdmittedFormats is the demuxers FFmpeg may use on a relayed input: those
// of the containers the scanner catalogs as video and audio
// (mediacontainer.Demuxers).
var AdmittedFormats = strings.Join(mediacontainer.Demuxers(), ",")

// The FFmpeg input options that restrict a relayed input.
const (
	protocolWhitelistOption = "-protocol_whitelist"
	formatWhitelistOption   = "-format_whitelist"
	httpProxyOption         = "-http_proxy"
)

// inputRestrictions are the input options that hold FFmpeg or ffprobe to one
// relayed input. FFmpeg's http protocol sends a request through the proxy in
// the http_proxy environment variable, loopback addresses included, and the
// protocol whitelist does not stop it: on a host with http_proxy set and no
// no_proxy for 127.0.0.1, every relayed read would go to that proxy, fail,
// and leave the relay URL, a bearer capability, in its logs. An empty
// -http_proxy makes FFmpeg connect to the relay directly (FFmpeg uses a
// proxy only when the value starts with http://).
func inputRestrictions() []string {
	return []string{protocolWhitelistOption, ProtocolWhitelist, formatWhitelistOption, AdmittedFormats, httpProxyOption, ""}
}

// restrict returns argv with every relay URL input held to the relay's
// protocols and the admitted demuxers, the options placed just before it:
// before its "-i", or for ffprobe (probe) before a positional relay URL too.
// It fails closed: a relay URL whose source is gone is restricted too. An
// argv with no relay URL input, such as any local path, is returned
// unchanged.
func restrict(args []string, probe bool) []string {
	var out []string
	copied, previous := 0, ""
	for i, arg := range args {
		afterInputFlag := previous == "-i"
		previous = arg
		if !IsRelayURL(arg) {
			continue
		}
		at := i
		if afterInputFlag {
			at = i - 1
		} else if !probe {
			continue
		}
		if out == nil {
			out = make([]string, 0, len(args)+len(inputRestrictions()))
		}
		out = append(append(out, args[copied:at]...), inputRestrictions()...)
		copied = at
	}
	if out == nil {
		return args
	}
	return append(out, args[copied:]...)
}
