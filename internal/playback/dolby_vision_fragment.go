package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

const maxDVFragmentBytes = 128 << 20

// Limit decoded conversion memory and child processes across playback sessions.
var dvConversionSlots = make(chan struct{}, 2)

// ConvertDV7Fragment replaces RPUs and removes the enhancement layer in place;
// remuxing raw HEVC would lose the copied samples' timestamps and durations.
// Input must be a single, clear, video-only fMP4 fragment and its original init.
func ConvertDV7Fragment(ctx context.Context, toolPath string, initData, fragmentData []byte) ([]byte, []byte, error) {
	return convertDV7FragmentWithPolicy(ctx, toolPath, initData, fragmentData, nil)
}

func convertDV7FragmentWithPolicy(ctx context.Context, toolPath string, initData, fragmentData []byte, policy *TranscodePolicy) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(initData) > 2<<20 || len(fragmentData) > maxDVFragmentBytes {
		return nil, nil, errors.New("dolby vision fragment exceeds conversion limit")
	}
	select {
	case dvConversionSlots <- struct{}{}:
		defer func() { <-dvConversionSlots }()
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return convertDV7FragmentWithSlot(ctx, toolPath, initData, fragmentData, policy)
}

// The caller holds dvConversionSlots for the lifetime of the source buffers.
func convertDV7FragmentWithSlot(ctx context.Context, toolPath string, initData, fragmentData []byte, policy *TranscodePolicy) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	source, err := decodeDV7Fragment(initData, fragmentData)
	if err != nil {
		return nil, nil, err
	}
	annexB, sourceNALs, rpuCount, err := prepareDV7NALs(ctx, source.entry, source.samples)
	if err != nil {
		return nil, nil, err
	}
	converted, err := convertDV7AnnexBWithPolicy(ctx, toolPath, annexB, policy)
	if err != nil {
		return nil, nil, err
	}
	rpus := dolbyVisionRPUs(converted)
	if len(rpus) != rpuCount {
		return nil, nil, errors.New("dolby vision conversion changed RPU count")
	}
	output, err := rebuildDV7Samples(ctx, source, sourceNALs, rpus)
	if err != nil {
		return nil, nil, err
	}
	replaceDV7Configuration(source.entry)
	var outInit, outFragment bytes.Buffer
	if err := source.init.Encode(&outInit); err != nil {
		return nil, nil, err
	}
	if err := output.Encode(&outFragment); err != nil {
		return nil, nil, err
	}
	return outInit.Bytes(), outFragment.Bytes(), nil
}

type dv7FragmentSource struct {
	init     *mp4.InitSegment
	entry    *mp4.VisualSampleEntryBox
	fragment *mp4.Fragment
	samples  []mp4.FullSample
}

func decodeDV7Initialization(initData []byte) (*mp4.InitSegment, *mp4.VisualSampleEntryBox, error) {
	initFile, err := mp4.DecodeFile(bytes.NewReader(initData))
	if err != nil || initFile.Init == nil {
		return nil, nil, errors.New("invalid Dolby Vision initialization")
	}
	init := initFile.Init
	if init.Moov == nil || len(init.Moov.Traks) != 1 || init.Moov.Mvex == nil || init.Moov.Mvex.Trex == nil {
		return nil, nil, errors.New("dolby vision conversion requires one fragmented video track")
	}
	track := init.Moov.Trak
	if track == nil || track.Tkhd == nil || track.Mdia == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil {
		return nil, nil, errors.New("dolby vision track configuration is unavailable")
	}
	entry := track.Mdia.Minf.Stbl.Stsd.HvcX
	if entry == nil || entry.HvcC == nil || entry.HvcC.LengthSizeMinusOne != 3 || entry.DoViConfig == nil {
		return nil, nil, errors.New("dolby vision configuration is unavailable")
	}
	config := entry.DoViConfig
	if config.DVProfile != 7 || !config.RPUPresentFlag || !config.BLPresentFlag || config.DVLevel < 1 || config.DVLevel > 13 {
		return nil, nil, errors.New("unsupported Dolby Vision conversion source")
	}
	return init, entry, nil
}

func decodeDV7Fragment(initData, fragmentData []byte) (dv7FragmentSource, error) {
	var source dv7FragmentSource
	init, entry, err := decodeDV7Initialization(initData)
	if err != nil {
		return source, err
	}
	track := init.Moov.Trak
	file, err := mp4.DecodeFile(bytes.NewReader(fragmentData))
	if err != nil || len(file.Segments) != 1 || len(file.Segments[0].Fragments) != 1 {
		return source, errors.New("invalid Dolby Vision media fragment")
	}
	fragment := file.Segments[0].Fragments[0]
	if fragment.Moof == nil || fragment.Moof.Mfhd == nil || len(fragment.Moof.Trafs) != 1 || fragment.Moof.Trafs[0].Tfhd == nil || fragment.Moof.Trafs[0].Tfhd.TrackID != track.Tkhd.TrackID {
		return source, errors.New("dolby vision fragment track differs from initialization")
	}
	samples, err := fragment.GetFullSamples(init.Moov.Mvex.Trex)
	if err != nil || len(samples) == 0 {
		return source, errors.New("dolby vision fragment has no readable samples")
	}
	return dv7FragmentSource{init: init, entry: entry, fragment: fragment, samples: samples}, nil
}

func prepareDV7NALs(ctx context.Context, entry *mp4.VisualSampleEntryBox, samples []mp4.FullSample) ([]byte, [][][]byte, int, error) {
	var annexB bytes.Buffer
	startCode := []byte{0, 0, 0, 1}
	for _, array := range entry.HvcC.NaluArrays {
		for _, nalu := range array.Nalus {
			annexB.Write(startCode)
			annexB.Write(nalu)
		}
	}
	sourceNALs := make([][][]byte, len(samples))
	rpuCount := 0
	for i, sample := range samples {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		nalus, err := lengthPrefixedHEVCNALs(sample.Data)
		if err != nil {
			return nil, nil, 0, err
		}
		sourceNALs[i] = nalus
		for _, nalu := range nalus {
			annexB.Write(startCode)
			annexB.Write(nalu)
			if nalu[0]>>1&63 == 62 {
				rpuCount++
			}
		}
	}
	if rpuCount == 0 {
		return nil, nil, 0, errors.New("dolby vision fragment contains no RPU")
	}
	return annexB.Bytes(), sourceNALs, rpuCount, nil
}

func rebuildDV7Samples(ctx context.Context, source dv7FragmentSource, sourceNALs [][][]byte, rpus [][]byte) (*mp4.Fragment, error) {
	output, err := mp4.CreateFragment(source.fragment.Moof.Mfhd.SequenceNumber, source.init.Moov.Trak.Tkhd.TrackID)
	if err != nil {
		return nil, err
	}
	rpuIndex := 0
	for i, sample := range source.samples {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, nextRPU, err := rebuildDV7Sample(sourceNALs[i], rpus, rpuIndex)
		if err != nil {
			return nil, err
		}
		rpuIndex = nextRPU
		sampleSize := len(data)
		if sampleSize < 0 || sampleSize > math.MaxUint32 {
			return nil, errors.New("dolby vision sample exceeds size limit")
		}
		sample.Data = data
		sample.Size = uint32(sampleSize)
		output.AddFullSample(sample)
	}
	return output, nil
}

func rebuildDV7Sample(nalus, rpus [][]byte, rpuIndex int) ([]byte, int, error) {
	var data bytes.Buffer
	for _, nalu := range nalus {
		switch nalu[0] >> 1 & 63 {
		case 63:
			continue
		case 62:
			nalu = rpus[rpuIndex]
			rpuIndex++
		default:
			if (uint16(nalu[0]&1)<<5)|uint16(nalu[1]>>3) != 0 {
				return nil, rpuIndex, errors.New("unsupported Dolby Vision enhancement-layer layout")
			}
		}
		naluSize := len(nalu)
		if naluSize < 0 || naluSize > math.MaxUint32 {
			return nil, rpuIndex, errors.New("dolby vision NAL exceeds size limit")
		}
		if err := binary.Write(&data, binary.BigEndian, uint32(naluSize)); err != nil {
			return nil, rpuIndex, err
		}
		data.Write(nalu)
	}
	return data.Bytes(), rpuIndex, nil
}

func replaceDV7Configuration(entry *mp4.VisualSampleEntryBox) {
	config := entry.DoViConfig
	children := entry.Children[:0]
	for _, child := range entry.Children {
		if _, ok := child.(*mp4.DoViConfigurationBox); !ok {
			children = append(children, child)
		}
	}
	entry.Children = children
	entry.SetType(VideoSampleEntryDVH1)
	entry.AddChild(mp4.CreateDoViConfigurationBox(config.DVVersionMajor, config.DVVersionMinor, 8, config.DVLevel, true, false, true, 1))
}

func lengthPrefixedHEVCNALs(data []byte) ([][]byte, error) {
	var nalus [][]byte
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, errors.New("truncated HEVC NAL length")
		}
		size := binary.BigEndian.Uint32(data[:4])
		data = data[4:]
		if size < 2 || uint64(size) > uint64(len(data)) || size > math.MaxInt32 {
			return nil, errors.New("invalid HEVC NAL length")
		}
		nalus = append(nalus, data[:int(size)])
		data = data[int(size):]
	}
	return nalus, nil
}

func convertDV7AnnexB(ctx context.Context, toolPath string, data []byte) ([]byte, error) {
	return convertDV7AnnexBWithPolicy(ctx, toolPath, data, nil)
}

func convertDV7AnnexBWithPolicy(ctx context.Context, toolPath string, data []byte, policy *TranscodePolicy) ([]byte, error) {
	dir, err := os.MkdirTemp("", "silo-dv-convert-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	input, output := filepath.Join(dir, "input.hevc"), filepath.Join(dir, "output.hevc")
	if err := os.WriteFile(input, data, 0600); err != nil {
		return nil, err
	}
	// #nosec G204 G702 -- toolPath is resolved from the server PATH; arguments use private temporary files without a shell.
	cmd := exec.CommandContext(ctx, toolPath, "-m", "2", "convert", "--discard", "-i", input, "-o", output)
	if err := policy.check(); err != nil {
		return nil, err
	}
	err = cmd.Run()
	processmetrics.Record(processmetrics.Transcode, cmd.ProcessState, err, ctx.Err())
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("dolby vision converter failed: %w", err)
	}
	return readBoundedHLSFile(output, maxDVFragmentBytes)
}

func dolbyVisionRPUs(data []byte) [][]byte {
	// Normalize Annex B's three- and four-byte start codes before selecting RPUs.
	var rpus [][]byte
	start := -1
	for i := 0; i+3 <= len(data); {
		length := 0
		if i+4 <= len(data) && bytes.Equal(data[i:i+4], []byte{0, 0, 0, 1}) {
			length = 4
		} else if bytes.Equal(data[i:i+3], []byte{0, 0, 1}) {
			length = 3
		}
		if length == 0 {
			i++
			continue
		}
		if start >= 0 && i-start >= 2 && data[start]>>1&63 == 62 {
			rpus = append(rpus, data[start:i])
		}
		start = i + length
		i = start
	}
	if start >= 0 && len(data)-start >= 2 && data[start]>>1&63 == 62 {
		rpus = append(rpus, data[start:])
	}
	return rpus
}

// readBoundedHLSFile bounds allocations even if a producer replaces or grows
// the file between opening and reading it.
func readBoundedHLSFile(path string, limit int64) ([]byte, error) {
	// #nosec G304 -- callers supply server-owned HLS or private converter paths; LimitReader bounds the read.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || int64(len(data)) > limit {
		return nil, errors.New("HLS file exceeds read limit or is empty")
	}
	return data, nil
}
