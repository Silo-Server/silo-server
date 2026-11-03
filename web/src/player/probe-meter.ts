/**
 * The connection speed test's arithmetic (docs/design/2026-10-17-buffering-quality.md,
 * Connection speed test > Measuring), kept free of I/O so the shared
 * probe_cases vectors replay it unchanged.
 *
 * The clock starts at the first byte. Bytes count in the window in which they
 * arrive; window 0 is TCP ramp-up and never counts, a window with no bytes
 * counts at 0, and only complete windows count.
 */
export type ProbeMode = "quick" | "accurate";

export const PROBE_WINDOW_MS = 500;
/** A probe whose first byte has not arrived by then is abandoned. */
export const PROBE_FIRST_BYTE_DEADLINE_MS = 10_000;
/** The contract's floor for an estimate; a result under it measured nothing. */
export const MIN_PROBE_KBPS = 100;
/** The contract's ceiling for an estimate; a faster link is reported as a lower bound. */
export const MAX_PROBE_KBPS = 1_000_000;

interface ModeLimits {
  /** The probe `size` requested. */
  bytes: number;
  /** The longest test, after the first byte. */
  maxMs: number;
  /** How many final windows the settled rate is taken over. */
  settleWindows: number;
}

export const PROBE_MODES: Record<ProbeMode, ModeLimits> = {
  quick: { bytes: 100_000_000, maxMs: 3_000, settleWindows: 2 },
  accurate: { bytes: 1_000_000_000, maxMs: 10_000, settleWindows: 6 },
};

/** What a test reports: a rate, or with `lower_bound` only "at least kbps". */
export interface ProbeResult {
  kbps: number;
  lower_bound: boolean;
  bytes: number;
  duration_ms: number;
}

/** When the client stopped reading, and what it measured; null is nothing. */
export interface ProbeOutcome {
  stop: number;
  result: ProbeResult | null;
}

export class ProbeMeter {
  private readonly windows: number[] = [];
  /** Every byte received, for a transfer that ends before a window completes. */
  private total = 0;
  /** The last window already compared with the target. */
  private checked = 0;
  private done?: ProbeOutcome;
  readonly limits: ModeLimits;

  /** `targetKbps` stops a quick test early; the accurate test ignores it. */
  constructor(
    readonly mode: ProbeMode,
    private readonly targetKbps?: number,
  ) {
    this.limits = PROBE_MODES[mode];
  }

  /** The outcome once the test has stopped. */
  get outcome(): ProbeOutcome | undefined {
    return this.done;
  }

  /**
   * Records `bytes` arriving `t` ms after the first byte. Returns true when
   * the test has stopped and the client should close the probe.
   */
  add(t: number, bytes: number): boolean {
    if (this.tick(t)) return true;
    const window = Math.floor(t / PROBE_WINDOW_MS);
    this.windows[window] = (this.windows[window] ?? 0) + bytes;
    this.total += bytes;
    return false;
  }

  /**
   * Advances the clock to `t` without new bytes: a window that ended by then
   * may have beaten the target, and the time limit may have passed. Returns
   * true when the test has stopped.
   */
  tick(t: number): boolean {
    if (this.done) return true;
    if (t >= this.limits.maxMs) {
      this.finish(this.limits.maxMs);
      return true;
    }
    return this.earlyStop(t);
  }

  /**
   * Ends the test at `t` and returns what it measured. `transferEnded` says the
   * body ended cleanly at `t`, rather than the time limit cutting the test off.
   */
  finish(t: number, transferEnded = false): ProbeOutcome {
    if (this.done) return this.done;
    const stop = Math.min(t, this.limits.maxMs);
    if (this.earlyStop(stop)) return this.done!;
    const complete = Math.floor(stop / PROBE_WINDOW_MS);
    const first = Math.max(1, complete - this.limits.settleWindows);
    if (first >= complete) {
      // A link fast enough to deliver the whole probe before a window after
      // the ramp completes is measured over the whole transfer. The ramp is
      // inside that average, so it can only read low.
      if (transferEnded && t <= this.limits.maxMs && this.total >= this.limits.bytes) {
        const result = this.wholeTransfer(stop);
        return (this.done = { stop, result: result.kbps >= MIN_PROBE_KBPS ? result : null });
      }
      return (this.done = { stop, result: null });
    }
    let bytes = 0;
    for (let window = first; window < complete; window++) bytes += this.windows[window] ?? 0;
    const duration_ms = (complete - first) * PROBE_WINDOW_MS;
    const kbps = Math.floor((bytes * 8) / duration_ms);
    if (kbps < MIN_PROBE_KBPS) return (this.done = { stop, result: null });
    const result =
      kbps > MAX_PROBE_KBPS
        ? { kbps: MAX_PROBE_KBPS, lower_bound: true, bytes, duration_ms }
        : { kbps, lower_bound: false, bytes, duration_ms };
    return (this.done = { stop, result });
  }

  private wholeTransfer(stop: number): ProbeResult {
    // The transfer ends when the body ends, read from a sub-millisecond clock;
    // the results endpoint takes whole milliseconds.
    const duration_ms = Math.max(Math.round(stop), 1);
    const kbps = Math.floor((this.total * 8) / duration_ms);
    const target = this.mode === "quick" ? this.targetKbps : undefined;
    return {
      kbps: Math.min(target && kbps >= target ? target : kbps, MAX_PROBE_KBPS),
      lower_bound: true,
      bytes: this.total,
      duration_ms,
    };
  }

  /** A quick test stops at the end of the first window after window 0 that reaches the target. */
  private earlyStop(t: number): boolean {
    const target = this.targetKbps;
    if (this.mode !== "quick" || !target) return false;
    const complete = Math.floor(t / PROBE_WINDOW_MS);
    for (let window = Math.max(1, this.checked + 1); window < complete; window++) {
      this.checked = window;
      const bytes = this.windows[window] ?? 0;
      if (Math.floor((bytes * 8) / PROBE_WINDOW_MS) >= target) {
        // No result may report a target under the floor, so reaching one
        // measured nothing.
        this.done = {
          stop: (window + 1) * PROBE_WINDOW_MS,
          result:
            target >= MIN_PROBE_KBPS
              ? { kbps: target, lower_bound: true, bytes, duration_ms: PROBE_WINDOW_MS }
              : null,
        };
        return true;
      }
    }
    return false;
  }
}

/**
 * Replays a whole test from its chunk arrivals: `[t, bytes]` after the first
 * byte, and `end`, when the transfer ended (absent: it outlasted the test).
 */
export function measureProbe(
  mode: ProbeMode,
  targetKbps: number | undefined,
  chunks: readonly (readonly [number, number])[],
  end?: number,
): ProbeOutcome {
  const meter = new ProbeMeter(mode, targetKbps);
  for (const [t, bytes] of chunks) {
    if (end !== undefined && t > end) break;
    if (meter.add(t, bytes)) return meter.outcome!;
  }
  return end === undefined ? meter.finish(meter.limits.maxMs) : meter.finish(end, true);
}
