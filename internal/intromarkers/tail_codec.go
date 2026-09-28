package intromarkers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// creditsTailFormat names the credits_tail payload encoding.
const creditsTailFormat = "credits-tail:v1"

// Layout of a credits-tail:v1 payload, little-endian:
//
//	header   version uint8 (1), black threshold count B uint8,
//	         frame count uint32, silence count uint32
//	frame    offset ms uint32, B pblack uint8,
//	         YMIN YLOW YHIGH YMAX SATLOW SATHIGH SATMAX uint8,
//	         YAVG SATAVG uint16 in 1/256 steps
//	silence  start ms uint32, end ms uint32 (openSilenceEnd when open)
//
// Offsets count from the window start. The tail pass converts frames to
// 8-bit 4:2:0 before measuring them, so every statistic but the averages is
// a whole number from 0 to 255. Averages are rounded down, which keeps every
// comparison against a multiple of 1/256 as it was.
const (
	creditsTailVersion = 1
	tailHeaderBytes    = 10
	tailSilenceBytes   = 8
	averageScale       = 256
	openSilenceEnd     = math.MaxUint32
	// maxTailOffsetMS bounds offsets well inside uint32.
	maxTailOffsetMS = 1 << 31
)

func tailFrameBytes(thresholds int) int { return 4 + thresholds + 7 + 4 }

// creditsTail is the decoded tail pass of one file.
type creditsTail struct {
	Frames   []mediasample.FrameStats
	Silences []mediasample.Interval
}

// encodeCreditsTail encodes tail, whose times are absolute media seconds in
// a window that starts at windowStart. Every frame must carry thresholds
// black measurements.
func encodeCreditsTail(tail creditsTail, windowStart float64, thresholds int) ([]byte, error) {
	if thresholds > math.MaxUint8 {
		return nil, fmt.Errorf("%d black thresholds do not fit a credits tail", thresholds)
	}
	size := tailHeaderBytes + len(tail.Frames)*tailFrameBytes(thresholds) + len(tail.Silences)*tailSilenceBytes
	buf := make([]byte, 0, size)
	buf = append(buf, creditsTailVersion, byte(thresholds))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(tail.Frames)))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(tail.Silences)))
	for _, frame := range tail.Frames {
		if len(frame.PBlack) != thresholds {
			return nil, fmt.Errorf("frame at %.3f s has %d black measurements, want %d", frame.Seconds, len(frame.PBlack), thresholds)
		}
		offset, err := tailOffsetMS(frame.Seconds, windowStart)
		if err != nil {
			return nil, err
		}
		buf = binary.LittleEndian.AppendUint32(buf, offset)
		buf = append(buf, frame.PBlack...)
		for _, value := range []float32{frame.YMin, frame.YLow, frame.YHigh, frame.YMax, frame.SatLow, frame.SatHigh, frame.SatMax} {
			buf = append(buf, byte(math.Round(clamp(float64(value), 0, math.MaxUint8))))
		}
		for _, value := range []float32{frame.YAvg, frame.SatAvg} {
			scaled := math.Floor(float64(value) * averageScale)
			buf = binary.LittleEndian.AppendUint16(buf, uint16(clamp(scaled, 0, math.MaxUint16)))
		}
	}
	for _, silence := range tail.Silences {
		start, err := tailOffsetMS(silence.Start, windowStart)
		if err != nil {
			return nil, err
		}
		end := uint32(openSilenceEnd)
		if silence.End > 0 {
			if end, err = tailOffsetMS(silence.End, windowStart); err != nil {
				return nil, err
			}
		}
		buf = binary.LittleEndian.AppendUint32(buf, start)
		buf = binary.LittleEndian.AppendUint32(buf, end)
	}
	return buf, nil
}

// decodeCreditsTail decodes a payload encodeCreditsTail wrote for a window
// that starts at windowStart.
func decodeCreditsTail(payload []byte, windowStart float64) (creditsTail, error) {
	if len(payload) < tailHeaderBytes {
		return creditsTail{}, errors.New("credits tail payload is too short")
	}
	if payload[0] != creditsTailVersion {
		return creditsTail{}, fmt.Errorf("credits tail payload version %d is not supported", payload[0])
	}
	thresholds := int(payload[1])
	frames := int(binary.LittleEndian.Uint32(payload[2:]))
	silences := int(binary.LittleEndian.Uint32(payload[6:]))
	frameBytes := tailFrameBytes(thresholds)
	body := payload[tailHeaderBytes:]
	if frames > len(body)/frameBytes || silences > len(body)/tailSilenceBytes ||
		len(body) != frames*frameBytes+silences*tailSilenceBytes {
		return creditsTail{}, fmt.Errorf("credits tail payload of %d bytes does not hold %d frames and %d silences", len(payload), frames, silences)
	}
	seconds := func(ms uint32) float64 { return windowStart + float64(ms)/1000 }
	tail := creditsTail{Frames: make([]mediasample.FrameStats, frames)}
	for i := range tail.Frames {
		record := body[i*frameBytes : (i+1)*frameBytes]
		stats := record[4+thresholds:]
		tail.Frames[i] = mediasample.FrameStats{
			Seconds: seconds(binary.LittleEndian.Uint32(record)),
			PBlack:  append([]uint8(nil), record[4:4+thresholds]...),
			YMin:    float32(stats[0]),
			YLow:    float32(stats[1]),
			YHigh:   float32(stats[2]),
			YMax:    float32(stats[3]),
			SatLow:  float32(stats[4]),
			SatHigh: float32(stats[5]),
			SatMax:  float32(stats[6]),
			YAvg:    float32(binary.LittleEndian.Uint16(stats[7:])) / averageScale,
			SatAvg:  float32(binary.LittleEndian.Uint16(stats[9:])) / averageScale,
		}
	}
	body = body[frames*frameBytes:]
	if silences > 0 {
		tail.Silences = make([]mediasample.Interval, silences)
	}
	for i := range tail.Silences {
		record := body[i*tailSilenceBytes:]
		interval := mediasample.Interval{Start: seconds(binary.LittleEndian.Uint32(record))}
		if end := binary.LittleEndian.Uint32(record[4:]); end != openSilenceEnd {
			interval.End = seconds(end)
		}
		tail.Silences[i] = interval
	}
	return tail, nil
}

// tailOffsetMS is seconds as whole milliseconds after windowStart. A time
// just before the window, from rounding, counts as its start.
func tailOffsetMS(seconds, windowStart float64) (uint32, error) {
	offset := math.Round((seconds - windowStart) * 1000)
	if math.IsNaN(offset) || offset >= maxTailOffsetMS {
		return 0, fmt.Errorf("time %.3f s is outside the credits tail window starting at %.3f s", seconds, windowStart)
	}
	return uint32(max(0, offset)), nil
}

func clamp(value, low, high float64) float64 {
	return math.Max(low, math.Min(high, value))
}
