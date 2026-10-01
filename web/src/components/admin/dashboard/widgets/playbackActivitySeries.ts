import type { AdminPlaybackActivityBucket } from "@/api/types";
import { timeBuckets } from "../charts";
import type { StackedColumnBucket } from "../charts";

const HOUR_MS = 3_600_000;

/** Bucket width the endpoint falls back to when a response predates the field. */
export const DEFAULT_PLAYBACK_BUCKET_SECONDS = 3600;

/**
 * Series order is fixed by entity, not by size: direct play always sits at the
 * baseline in `--chart-1`, direct stream above it, transcode on top. "Remux" is
 * the server's word for a direct stream; the dashboard uses the operator's.
 */
export const PLAYBACK_SERIES_LABELS = ["Direct play", "Direct stream", "Transcode"] as const;

const EMPTY_SEGMENTS: readonly number[] = [0, 0, 0];

export interface PlaybackActivityColumnOptions {
  /** Window length in hours, matching the `hours` the endpoint was asked for. */
  hours?: number;
  /** Bucket width the endpoint grouped by, from `bucket_seconds`. */
  bucketSeconds?: number;
  now?: number;
}

/**
 * The columns of the window, oldest first.
 *
 * The endpoint returns only buckets that saw a session, so quiet ones have to
 * be filled in here — a stacked column chart that silently skipped them would
 * compress the window and misplace every remaining column on the axis. The
 * bucket width comes from the response rather than being assumed hourly: past
 * two days the server groups by day, and zero-filling on the wrong grid would
 * scatter the real columns between empty ones.
 *
 * Both edges are snapped to the bucket grid the server truncates on, so the
 * leading bucket is the one that contains `now - hours`, partial or not. At
 * 12:30 an hourly one-hour window covers 11:00 and 12:00, exactly the two
 * buckets the endpoint's `started_at >= now() - interval` filter can return;
 * starting at 12:00 instead would drop the sessions from 11:30 to 12:00.
 */
export function buildPlaybackActivityColumns(
  buckets: readonly AdminPlaybackActivityBucket[] | undefined,
  options: PlaybackActivityColumnOptions = {},
): StackedColumnBucket[] {
  const { hours = 24, bucketSeconds = DEFAULT_PLAYBACK_BUCKET_SECONDS, now = Date.now() } = options;
  const stepMs =
    bucketSeconds > 0 ? bucketSeconds * 1_000 : DEFAULT_PLAYBACK_BUCKET_SECONDS * 1_000;

  const to = Math.floor(now / stepMs) * stepMs;
  const from = Math.min(to, Math.floor((now - Math.max(0, hours) * HOUR_MS) / stepMs) * stepMs);
  const samples = (buckets ?? []).flatMap((bucket) => {
    const t = Date.parse(bucket.hour);
    if (!Number.isFinite(t)) {
      return [];
    }
    return [
      {
        t: Math.floor(t / stepMs) * stepMs,
        value: [bucket.direct, bucket.remux, bucket.transcode] as readonly number[],
      },
    ];
  });

  return timeBuckets<readonly number[]>(from, to, stepMs, samples, EMPTY_SEGMENTS).map(
    (bucket) => ({
      t: bucket.t,
      segments: bucket.value,
    }),
  );
}
