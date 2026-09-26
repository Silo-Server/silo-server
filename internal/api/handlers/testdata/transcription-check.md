# Transcription connection probe

`transcription-check.wav` contains synthetic speech saying "This is a subtitle
test." It contains no user recording. The connection check uploads it to the
configured speech provider and requires a nonempty timed segment.

Generated with eSpeak NG 1.52.0 (`en-us` voice, 155 words per minute), then
converted with FFmpeg to 16 kHz mono 16-bit PCM WAV with metadata removed.
Commands assume the repository root is the cwd:

```sh
espeak-ng -v en-us -s 155 -w internal/api/handlers/testdata/source.wav \
  'This is a subtitle test.'
ffmpeg -i internal/api/handlers/testdata/source.wav -map_metadata -1 -ac 1 -ar 16000 \
  -c:a pcm_s16le -fflags +bitexact -flags:a +bitexact \
  internal/api/handlers/testdata/transcription-check.wav
```
