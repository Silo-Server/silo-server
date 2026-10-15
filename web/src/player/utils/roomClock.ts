/**
 * Server clock estimation for a watch-together room. Each ping/pong exchange
 * yields one sample; the offset in use comes from the sample with the lowest
 * round trip, whose midpoint estimate has the least room for asymmetric delay.
 * Server time = local time + offset.
 */

/** Samples kept, newest last. */
export const roomClockMaxSamples = 8;
/** A round trip longer than this says more about the network than the clock. */
export const roomClockMaxRttMs = 5_000;
/**
 * Timestamps carry whole milliseconds, so a LAN round trip can come out a
 * little negative. Down to this much it counts as zero; below, it is a clock step.
 */
export const roomClockRttTruncationMs = 2;
/**
 * Slack on top of two samples' half round trips before their offsets are
 * taken to disagree, which means the local or server clock stepped.
 */
export const roomClockStepToleranceMs = 50;
/** Pings come this often until a connection has `roomClockFastSamples` samples. */
export const roomClockFastPingMs = 2_000;
export const roomClockFastSamples = 4;
/** Ping cadence once the estimate has settled. */
export const roomClockSlowPingMs = 15_000;

export interface RoomClockSample {
  rttMs: number;
  offsetMs: number;
}

/**
 * The sample one pong yields, or null when the timestamps are unusable or the
 * round trip is negative (clock steps) or longer than `roomClockMaxRttMs`.
 */
export function roomClockSample(
  clientSentMs: number,
  serverReceivedMs: number,
  serverSentMs: number,
  clientReceivedMs: number,
): RoomClockSample | null {
  if (
    !Number.isFinite(clientSentMs) ||
    !Number.isFinite(serverReceivedMs) ||
    !Number.isFinite(serverSentMs) ||
    !Number.isFinite(clientReceivedMs)
  ) {
    return null;
  }
  let rttMs = clientReceivedMs - clientSentMs - (serverSentMs - serverReceivedMs);
  if (rttMs < 0 && rttMs >= -roomClockRttTruncationMs) rttMs = 0;
  if (rttMs < 0 || rttMs > roomClockMaxRttMs) return null;
  const offsetMs = (serverReceivedMs - clientSentMs + (serverSentMs - clientReceivedMs)) / 2;
  return { rttMs, offsetMs };
}

/**
 * `samples` with `sample` appended, keeping the newest `roomClockMaxSamples`.
 * Each sample bounds the true offset to within half its round trip. When the
 * new sample and the current best cannot both be right, a clock moved: the
 * older samples describe a clock that no longer exists and are dropped.
 */
export function addRoomClockSample(
  samples: readonly RoomClockSample[],
  sample: RoomClockSample,
): RoomClockSample[] {
  if (roomClockStepped(samples, sample)) return [sample];
  return [...samples, sample].slice(-roomClockMaxSamples);
}

/** Whether `sample` and the best of `samples` cannot both be right: a clock stepped. */
export function roomClockStepped(
  samples: readonly RoomClockSample[],
  sample: RoomClockSample,
): boolean {
  const best = bestRoomClockSample(samples);
  return (
    best !== null &&
    Math.abs(sample.offsetMs - best.offsetMs) >
      (sample.rttMs + best.rttMs) / 2 + roomClockStepToleranceMs
  );
}

/**
 * The offset of the minimum-RTT sample, or null without samples. Among equal
 * round trips the newest wins, since clocks drift.
 */
export function roomClockOffsetMs(samples: readonly RoomClockSample[]): number | null {
  return bestRoomClockSample(samples)?.offsetMs ?? null;
}

function bestRoomClockSample(samples: readonly RoomClockSample[]): RoomClockSample | null {
  let best: RoomClockSample | null = null;
  for (const sample of samples) {
    if (best === null || sample.rttMs <= best.rttMs) best = sample;
  }
  return best;
}

/** Delay before the next ping, given the samples this connection has taken. */
export function roomClockPingDelayMs(connectionSamples: number): number {
  return connectionSamples < roomClockFastSamples ? roomClockFastPingMs : roomClockSlowPingMs;
}
