package playback

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os/exec"
	"time"

	"github.com/Eyevinn/mp4ff/avc"
)

// The base layer is the synthetic Main 10 tone-map probe frame. The RPU is
// factual codec metadata from FFmpeg's public mov/dovi-p7.mp4 FATE vector.
// This checks conversion and base-byte preservation, not display quality.
const dv7ProbeBaseLayer = "AAAAAUABDAH//wIgAAADALAAAAMAAAMAPAjAkAAAAAFCAQECIAAAAwCwAAADAAADADygCAgEBNiAjuRZFL/y5/E/rAWoEBAQBAAAAAFEAcBy8FMkAAAAAU4BBTJHVkrcXExDP5TvxRE80UOoAQAAAwABAwAAAwABAgADX/8LAAADAAADAAErEAwDkSsBDf////+AAAAAASgBrxAgmwzKq+iGPv/9kz4R2XADPFSQkYVLvPxAuVCAAuhvgvlhUQRRzuAARkAHHgAADpgAAAMAATcAAAMADvgAAAMAY0A="
const dv7ProbeRPU = "fAEZCAkIQGE2UK4gACAIAgCAIAgCAH+AH/wA/8AB//oAAAMBAAADANAAAAgAAAaAAABAAAA0AAADAgAAAwGgAAAQAAANAAADAIAAAGgAAAQAAAMDQAAAIAAACobZ5nsvhj78GJ5gKemMBrjFZqfNX43/y22MOeOKmYG4bKZ5D0BfQ0y32A6eBMNJ/Tu3fsLNXeOhR9RMCi6AYhZKAptdWVAO049YxGxOwToUWCz/MCNTbVe7tuQRSDuSlmQTaY/Oa8Mb1iD63UBbvvRkMWCx8Bvy7vvypeLzzLSr88w5g3eedWgxGwBIAABABABAAABAEgAAEAEAEAAAEASAAAQAQAQAAAclZgAANeolZvn86xwlZkTKAAADAQAAAwAIAAADAAgAAAMAHDYiQwGGCl4wjgUUAAADAaY+Wv//AAADAAADAAADAABgIA+A4VGAMAgAWcoSAMAoIY34JYAIAGFAAAICKlEgiAUAAAMAAigRUBIMB9AAAg1gAV648p2CgAA="
const dv81ProbeRPUHash = "65668e57535cdd71711c37e6f5cedb9069f9189cad192167157a68152ddbf33b"

func probeDV7ToDV81ToolchainV3(ctx context.Context) (string, bool) {
	tool, err := exec.LookPath("dovi_tool")
	if err != nil {
		return "", false
	}
	base, err := base64.StdEncoding.DecodeString(dv7ProbeBaseLayer)
	if err != nil {
		return "", false
	}
	rpu, err := base64.StdEncoding.DecodeString(dv7ProbeRPU)
	if err != nil {
		return "", false
	}
	input := append(append(append([]byte(nil), base...), 0, 0, 0, 1), rpu...)
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := convertDV7AnnexB(probeCtx, tool, input)
	if err != nil {
		return "", false
	}
	rpus := dolbyVisionRPUs(output)
	if len(rpus) != 1 || fmt.Sprintf("%x", sha256.Sum256(rpus[0])) != dv81ProbeRPUHash {
		return "", false
	}
	before, err := lengthPrefixedHEVCNALs(avc.ConvertByteStreamToNaluSample(base))
	if err != nil {
		return "", false
	}
	after, err := lengthPrefixedHEVCNALs(avc.ConvertByteStreamToNaluSample(output))
	if err != nil {
		return "", false
	}
	var copied [][]byte
	for _, nalu := range after {
		if nalu[0]>>1&63 != 62 {
			copied = append(copied, nalu)
		}
	}
	if len(before) != len(copied) {
		return "", false
	}
	for i := range before {
		if !bytes.Equal(before[i], copied[i]) {
			return "", false
		}
	}
	return tool, true
}
