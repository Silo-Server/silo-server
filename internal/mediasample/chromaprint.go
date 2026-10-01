package mediasample

import "encoding/binary"

// DecodeRawFingerprint reads the chromaprint muxer's raw output (fp_format
// raw): little-endian 32-bit points. A trailing partial point is dropped.
func DecodeRawFingerprint(output []byte) []uint32 {
	if len(output) < 4 {
		return nil
	}
	points := make([]uint32, 0, len(output)/4)
	for len(output) >= 4 {
		points = append(points, binary.LittleEndian.Uint32(output[:4]))
		output = output[4:]
	}
	return points
}

// EncodeRawFingerprint writes points in the chromaprint muxer's raw format.
func EncodeRawFingerprint(points []uint32) []byte {
	buf := make([]byte, len(points)*4)
	for i, point := range points {
		binary.LittleEndian.PutUint32(buf[i*4:], point)
	}
	return buf
}
