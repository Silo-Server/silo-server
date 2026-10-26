// @vitest-environment node
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { ALL_FORMATS, BufferSource, EncodedPacketSink, Input } from "mediabunny";
import { FFmpegAudioDecoder, loadFFmpegAudioDecoders } from "./ffmpegAudioDecoder";
import { SPEAKER, speakersFromMask } from "./audioMix";
import type { FFmpegAudioCodec } from "./codecs";

const here = (name: string) => fileURLToPath(new URL(name, import.meta.url));
const wasm = readFileSync(here("./wasm/silo-audio-decoders.wasm"));

function rms(samples: Float32Array): number {
  let sum = 0;
  for (const sample of samples) sum += sample * sample;
  return Math.sqrt(sum / Math.max(1, samples.length));
}

// Each fixture is 0.25 s of a 1 kHz tone (amplitude 1/8, RMS 0.088) on the
// center channel of a 5.1(side) layout with silence elsewhere; MLP carries a
// stereo downmix of the same signal, so the tone sits equally on both sides.
describe("FFmpegAudioDecoder", () => {
  const cases: Array<[FFmpegAudioCodec, string, number]> = [
    ["ac3", "ac3.mka", 6],
    ["eac3", "eac3.mka", 6],
    ["dts", "dts.mka", 6],
    ["truehd", "truehd.mka", 6],
    ["mlp", "mlp.mka", 2],
  ];

  for (const [codec, file, channels] of cases) {
    it(`decodes ${codec} with its channel layout`, async () => {
      const module = await loadFFmpegAudioDecoders({ wasmBinary: wasm });
      const input = new Input({
        formats: ALL_FORMATS,
        source: new BufferSource(readFileSync(here(`./testdata/${file}`))),
      });
      const track = (await input.getAudioTracks())[0]!;
      const decoder = new FFmpegAudioDecoder(
        module,
        codec,
        track.sampleRate,
        track.numberOfChannels,
      );
      const planes: Float32Array[][] = [];
      let mask = 0;
      let firstTimestamp: number | null = null;
      for await (const packet of new EncodedPacketSink(track).packets()) {
        for (const frame of decoder.decode(packet.data, packet.timestamp * 1e6)) {
          firstTimestamp ??= frame.timestampUs;
          mask = frame.channelMask;
          expect(frame.sampleRate).toBe(48_000);
          planes.push(frame.channels);
        }
      }
      for (const frame of decoder.drain()) planes.push(frame.channels);
      decoder.close();

      const total = planes.reduce((sum, frame) => sum + frame[0]!.length, 0);
      expect(total / 48_000).toBeGreaterThan(0.2);
      expect(firstTimestamp).toBe(0);
      const speakers = speakersFromMask(mask, channels);
      expect(speakers).toHaveLength(channels);
      const joined = (speaker: number) => {
        const channel = speakers.indexOf(speaker);
        const out = new Float32Array(total);
        let offset = 0;
        for (const frame of planes) {
          out.set(frame[channel]!, offset);
          offset += frame[channel]!.length;
        }
        return out;
      };
      if (channels === 6) {
        const tone = rms(joined(SPEAKER.FC));
        expect(tone).toBeGreaterThan(0.08);
        // The tone stays on its own speaker; lossy codecs leak only noise.
        expect(rms(joined(SPEAKER.FL))).toBeLessThan(tone / 20);
        expect(rms(joined(SPEAKER.SR))).toBeLessThan(tone / 20);
      } else {
        const left = rms(joined(SPEAKER.FL));
        expect(left).toBeGreaterThan(0.02);
        expect(rms(joined(SPEAKER.FR))).toBeCloseTo(left, 3);
      }
    });
  }

  it("refuses to open an unknown codec id", async () => {
    const module = await loadFFmpegAudioDecoders({ wasmBinary: wasm });
    expect(() => new FFmpegAudioDecoder(module, "nope" as FFmpegAudioCodec, 48_000, 2)).toThrow();
  });
});
