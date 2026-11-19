package mediasample

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"testing"
)

func speechRequest(center bool) Request {
	return Request{
		Input:  "/media/a.mkv",
		Window: &Window{StartSeconds: 600, DurationSeconds: 120},
		Audio:  &AudioOutput{Speech: &SpeechParams{AudioStream: 1, CenterChannel: center}},
	}
}

// pcm encodes samples as s16le.
func pcm(samples []int16) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(s))
	}
	return out
}

func tone(n int, amplitude float64) []int16 {
	samples := make([]int16, n)
	for i := range samples {
		samples[i] = int16(amplitude * math.Sin(2*math.Pi*440*float64(i)/speechSampleRate))
	}
	return samples
}

func TestRunSpeech(t *testing.T) {
	req := speechRequest(false)
	runner := Runner{FFmpegPath: "ffmpeg", Exec: fakeExec(func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, err := stdout.Write(pcm(tone(3*speechFrameSamples, 32767)))
		return err
	})}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Speech == nil || len(result.Speech.Levels) != 3 || result.Speech.StartSeconds != 600 {
		t.Fatalf("speech %+v", result.Speech)
	}
}
