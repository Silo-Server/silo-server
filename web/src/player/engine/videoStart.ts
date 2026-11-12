/**
 * Where a video decode run starts for a position.
 *
 * On Chromium, Mediabunny verifies an H.264 key packet only when it holds an
 * IDR slice, and walks back one key packet at a time, without limit, until it
 * finds one. In an open-GOP stream, as on Blu-ray, the only IDR can be at the
 * very start, so a resume would read most of the file before showing
 * anything. H.264 instead starts at the container's key packet when it is an
 * IDR or an I-frame with a recovery point, and steps back past one that is
 * neither (some containers mark every frame as a key) or that the decoder
 * rejects: some encoders let the pictures after a recovery point reference
 * the GOP before it, and decoding from an earlier key packet gives them those
 * references. Other codecs use Mediabunny's verified lookup.
 */
import type { EncodedPacket, EncodedPacketSink } from "mediabunny";
import { h264RandomAccess } from "./h264";

export interface VideoStart {
  packet: EncodedPacket;
  /** An H.264 I-frame with a recovery point rather than an IDR. */
  recoveryPoint: boolean;
}

/**
 * Bounds on the H.264 search: how many starts it yields, and how far behind
 * the position it steps past key packets it cannot start on. A start it can
 * decode from is yielded however far back it is.
 */
export const MAX_H264_START_ATTEMPTS = 8;
export const MAX_H264_START_LOOKBACK_SECONDS = 20;

/**
 * The starts to try for `target`, nearest first. The caller asks for the
 * next only when the decoder rejected the last. Returns "no-keys" when the
 * track has no key packet at all, and "exhausted" when no start within the
 * bounds is left. `h264LengthSize` is the NAL length-field size of an H.264
 * track, or null for any other video. The search stops, as exhausted, once
 * `cancelled` returns true, so a newer run does not wait behind it.
 */
export async function* videoStarts(
  sink: EncodedPacketSink,
  target: number,
  h264LengthSize: number | null,
  cancelled: () => boolean,
): AsyncGenerator<VideoStart, "no-keys" | "exhausted"> {
  if (h264LengthSize === null) {
    const packet =
      (await sink.getKeyPacket(target, { verifyKeyPackets: true })) ??
      (await sink.getFirstKeyPacket({ verifyKeyPackets: true }));
    if (!packet) return "no-keys";
    yield { packet, recoveryPoint: false };
    return "exhausted";
  }
  let before = target;
  let attempts = 0;
  for (let first = true; attempts < MAX_H264_START_ATTEMPTS; first = false) {
    if (cancelled()) return "exhausted";
    const packet =
      (await sink.getKeyPacket(before)) ?? (first ? await sink.getFirstKeyPacket() : null);
    if (!packet) {
      if (first) return "no-keys";
      // Back at the start of the file without a start to try, as in a
      // recording cut mid-GOP: start at the first key the verified lookup
      // accepts, which can be after the position.
      const key = attempts === 0 ? await sink.getFirstKeyPacket({ verifyKeyPackets: true }) : null;
      if (key) yield { packet: key, recoveryPoint: false };
      return "exhausted";
    }
    const access = h264RandomAccess(packet.data, h264LengthSize);
    if (access) {
      attempts += 1;
      yield { packet, recoveryPoint: access === "recovery-point" };
    } else if (packet.timestamp < target - MAX_H264_START_LOOKBACK_SECONDS) {
      return "exhausted";
    }
    before = packet.timestamp - 0.001;
  }
  return "exhausted";
}
