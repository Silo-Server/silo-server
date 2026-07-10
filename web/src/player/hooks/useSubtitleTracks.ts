import { useEffect, useRef, useState } from "react";
import { parseVTT, type ParsedCue } from "../utils/parseVTT";
import type { PlayerSubtitleInfo } from "../types";
import { isASSCodec, isBitmapCodec } from "../utils/subtitleCodecs";
import { toMediaTime } from "../utils/mediaTimeline";

// Each subtitle fetch covers this many source-time seconds. Matches the
// server's default `?duration=`; if you raise one, raise the other.
const WINDOW_DURATION = 600;
// Start fetching the next window this many seconds before the current
// one's requested end, so the new cues are already on hand by the time
// playback crosses into them.
const PREFETCH_LEAD = 30;
// Overlap consecutive windows by a few seconds so ffmpeg boundary
// rounding can't drop a cue that straddles the join.
const WINDOW_OVERLAP = 5;
// Pull back this many seconds from the current position when starting
// a fresh fetch, so a quick scrub back still lands inside the window.
const SEEK_BACKOFF = 2;
// Abort a window fetch when the response goes this long without delivering
// a chunk. Extraction streams cues progressively, so a healthy-but-slow
// ffmpeg keeps resetting the clock; only a genuinely hung one trips it.
// Without this, one hung fetch blocks every future window for the session.
const FETCH_STALL_TIMEOUT_MS = 30_000;
// Wait this long after a failed window fetch before retrying, so a
// persistently failing extraction doesn't turn timeupdate into a fetch storm.
const FETCH_RETRY_BACKOFF_MS = 5_000;

/**
 * Cues (in source time) and window coverage snapshotted from a track that is
 * being torn down, so a rebuild against a reloaded <video> element (stream
 * restart) can restore them instead of refetching — window extraction costs
 * a multi-second ffmpeg run per request.
 */
interface SubtitleTrackCarryover {
  url: string | null;
  cues: ParsedCue[];
  seen: Set<string>;
  coverageStart: number;
  windowEnd: number;
  atEOF: boolean;
  hasFetched: boolean;
}

/** Strip VTT formatting tags, keeping only the text content. */
function stripVTTTags(text: string): string {
  return text.replace(/<[^>]+>/g, "");
}

/**
 * Add parsed cues to a TextTrack, applying the stream origin and user delay and
 * deduping against `seen`. Shared by the URL fetcher and the live-cue path.
 */
function addCuesToTrack(
  track: TextTrack,
  cues: ParsedCue[],
  origin: number,
  delaySec: number,
  seen: Set<string>,
): void {
  for (const parsed of cues) {
    if (parsed.end <= parsed.start) continue;
    const startTime = Math.max(0, parsed.start - origin + delaySec);
    const endTime = parsed.end - origin + delaySec;
    if (endTime <= 0) continue;
    // Key on the source cue times, not the delay-shifted display times: the
    // delay can change between overlapping fetch windows, and a delay-relative
    // key would let the same source cue re-add itself after a shift.
    const key = `${parsed.start}|${parsed.end}|${parsed.text}`;
    if (seen.has(key)) continue;
    seen.add(key);
    track.addCue(new VTTCue(startTime, endTime, parsed.text));
  }
}

/** Append or replace the `position` query param on a subtitle URL. */
function appendPosition(url: string, position: number): string {
  const sep = url.includes("?") ? "&" : "?";
  return `${url}${sep}position=${position}`;
}

/**
 * Manages subtitle display by delegating cue-against-time matching to the
 * browser's TextTrack, while keeping render control on our side for the
 * subtitle appearance panel.
 *
 * We create a programmatic TextTrack in `hidden` mode, stream WebVTT from
 * the server incrementally, and push each parsed cue as a `VTTCue` onto
 * the track. The browser then fires `cuechange` in sync with the media
 * clock — the same clock that dictates which video frame is on screen —
 * so subtitles display exactly when they should regardless of any PTS
 * normalization hls.js has applied to the underlying segments.
 *
 * A sliding-window fetcher prefetches the next window as playback nears
 * the tail of the current one and aborts in-flight fetches on seeks
 * outside coverage. The fetch scheduling is independent from cue display.
 *
 * `subtitleDelayMs` lets the user nudge sync without a refetch: new cues
 * are added with the current delay baked in, and existing cues are shifted
 * in place whenever the value changes. Positive = show subtitles later.
 * `streamOriginSeconds` changes (a copy-mode session restarting at a new
 * position) are handled the same way: existing cues shift in place onto
 * the new timeline instead of tearing down the track.
 */
export function useSubtitleTracks(
  videoRef: React.RefObject<HTMLVideoElement | null>,
  subtitleUrls: PlayerSubtitleInfo[],
  activeSubtitleIndex: number | null,
  streamOriginSeconds: number,
  subtitleDelayMs: number,
  // Known media duration in source-time seconds (0 when unknown). Used to
  // decide when a fetch window has reached end-of-input.
  durationRef: React.RefObject<number>,
  // Media-time position playback is heading to (pending seek target, or the
  // session's start position). While the element has no media loaded yet
  // (session start or a stream restart), `video.currentTime` still reads 0
  // rather than the resume/seek target — fetching the window at 0 wastes an
  // ffmpeg run and, because only one fetch is in flight at a time, blocks
  // the correct window until it completes.
  fetchAnchorRef: React.RefObject<number>,
  liveCues?: ParsedCue[] | null,
  // Identifies the current live translation job. Changing it (a new job) rebuilds
  // the track and resets the dedup set so cues from a prior run never linger.
  liveTrackKey?: string | null,
  // Bumped whenever the underlying media stream is (re)started — a quality,
  // audio, or subtitle-burn-in switch tears down the <video> element and
  // reloads it. A programmatic TextTrack built moments before that reload is
  // orphaned by the browser (it survives quality switches only because it was
  // built against an already-stable stream). Changing this rebuilds the track
  // against the settled new stream, so selecting a text track in the same
  // action that restarts the transcode (turning off bitmap burn-in) still
  // renders. The initial stream does not bump it, leaving session start on the
  // existing activeUrl-driven build.
  streamGeneration = 0,
): string[] {
  const [activeCueTexts, setActiveCueTexts] = useState<string[]>([]);

  // Latest stream origin, readable from stable callbacks (maybeFetch) without
  // retriggering the main effect.
  const streamOriginRef = useRef(streamOriginSeconds);
  streamOriginRef.current = streamOriginSeconds;
  // Origin currently baked into the track's VTTCue times. Cue-add paths read
  // this (not the live origin) so the track never mixes bases; the rebase
  // effect below is the only place it advances, shifting existing cues along.
  const appliedOriginRef = useRef(streamOriginSeconds);

  const activeSub =
    activeSubtitleIndex !== null
      ? (subtitleUrls.find((s) => s.index === activeSubtitleIndex) ?? null)
      : null;
  const activeUrl = activeSub?.url ?? null;
  const activeCodec = activeSub?.codec;
  const activeLang = activeSub?.language ?? "";
  // A live track's cues arrive over the websocket (liveCues) instead of from a
  // URL; the main effect builds the track but skips the sliding-window fetcher.
  const activeIsLive = activeSub?.live === true;

  // Track which delay is currently baked into the VTTCues on the active track,
  // so the delay-update effect below can compute the exact shift to apply.
  // Cue-add paths also read this to keep new cues aligned with existing ones.
  const appliedDelayMsRef = useRef(0);
  const trackRef = useRef<TextTrack | null>(null);
  // Cue dedup set, held in a ref so the live-cue effect and the URL fetcher
  // share it. Reset whenever a fresh track is built.
  const seenCueKeysRef = useRef<Set<string>>(new Set());
  // How many of `liveCues` have already been pushed onto the live track. Lets the
  // live-cue effect add only the new tail each batch instead of rescanning the
  // whole (growing) array. Reset on every track rebuild.
  const processedLiveCuesRef = useRef(0);
  // Snapshot of the previous track's cues and coverage, written by the main
  // effect's cleanup and consumed by the next build when the subtitle URL is
  // unchanged (a rebuild forced by a stream reload rather than a track switch).
  const carryoverRef = useRef<SubtitleTrackCarryover | null>(null);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    const videoEl: HTMLVideoElement = video;

    setActiveCueTexts([]);

    // Skip entirely for ASS/SSA (JASSUB renders those via useASSSubtitles)
    // and bitmap codecs (PGS/DVD/DVB are burned into the video server-side;
    // rendering text cues for them would double up on screen).
    if (isASSCodec(activeCodec) || isBitmapCodec(activeCodec)) {
      return;
    }
    // Need either a URL to stream from or a live cue source.
    if (!activeUrl && !activeIsLive) {
      return;
    }

    // Programmatic TextTrack. `hidden` mode still maintains activeCues
    // and fires `cuechange` synchronously with the media clock, but
    // suppresses the browser's built-in cue renderer so the appearance
    // panel stays in charge of styling.
    const track = videoEl.addTextTrack("subtitles", "Silo", activeLang || undefined);
    track.mode = "hidden";
    trackRef.current = track;
    seenCueKeysRef.current = new Set();
    processedLiveCuesRef.current = 0;
    // Fresh track has no cues, so it trivially carries the current origin.
    appliedOriginRef.current = streamOriginRef.current;

    // Restore cues and coverage carried over from the previous track when the
    // URL is unchanged — i.e. this rebuild replaces a track orphaned by a
    // stream reload, not a track switch. Cue times were snapshotted in source
    // time and are re-derived against the current origin/delay here, so an
    // origin change across the reload lands correctly. The carried dedup set
    // is installed as-is (its keys are source-time based and stay valid).
    const carried = carryoverRef.current;
    carryoverRef.current = null;
    const restored = carried && carried.url === activeUrl && !activeIsLive ? carried : null;
    if (restored) {
      const origin = appliedOriginRef.current;
      const delaySec = appliedDelayMsRef.current / 1000;
      for (const cue of restored.cues) {
        const startTime = Math.max(0, cue.start - origin + delaySec);
        const endTime = cue.end - origin + delaySec;
        if (endTime <= 0) continue;
        track.addCue(new VTTCue(startTime, endTime, cue.text));
      }
      seenCueKeysRef.current = restored.seen;
    }

    let cancelled = false;
    let hasFetched = restored?.hasFetched ?? false;

    // Sliding-window coverage state. See fetchWindow for semantics.
    let coverageStart = restored?.coverageStart ?? 0;
    let windowEnd = restored?.windowEnd ?? 0;
    let atEOF = restored?.atEOF ?? false;
    let inflight: AbortController | null = null;
    // Set on a failed (errored or stalled) window fetch; maybeFetch waits out
    // a short backoff before retrying the uncovered range.
    let lastFetchFailureAt = 0;

    function handleCueChange() {
      const active = track.activeCues;
      if (!active || active.length === 0) {
        setActiveCueTexts([]);
        return;
      }
      setActiveCueTexts(Array.from(active).map((c) => stripVTTTags((c as VTTCue).text)));
    }

    track.addEventListener("cuechange", handleCueChange);

    function clearCues() {
      const cues = track.cues;
      if (!cues) return;
      // Snapshot first: removeCue mutates the live list, and indexing into
      // a shifting list on each iteration is O(n²).
      for (const cue of Array.from(cues)) {
        track.removeCue(cue);
      }
      seenCueKeysRef.current.clear();
    }

    function addParsedCues(newCues: ParsedCue[]) {
      if (newCues.length === 0) return;
      // Cue timestamps come from ffmpeg in source-PTS. For copy-mode HLS
      // the player timeline is rebased to start at `streamOriginSeconds`,
      // so subtract it. For regular transcodes origin is 0 and the
      // subtraction is a no-op. Use the origin already baked into the
      // track (not the live one) so cues added while an origin change is
      // still propagating stay consistent with the existing cues — the
      // rebase effect shifts everything to the new origin in one pass.
      // Any active user-facing sync delay gets baked in here so new cues
      // line up with existing ones.
      const origin = appliedOriginRef.current;
      const delaySec = appliedDelayMsRef.current / 1000;
      addCuesToTrack(track, newCues, origin, delaySec, seenCueKeysRef.current);
    }

    async function fetchWindow(seekStart: number, resetExisting: boolean) {
      if (!activeUrl) return;

      // Only one window on the wire at a time: each fetch keeps an
      // ffmpeg process alive, double-fetching wastes that.
      inflight?.abort();
      const controller = new AbortController();
      inflight = controller;

      const requestedEnd = seekStart + WINDOW_DURATION;
      if (resetExisting) {
        clearCues();
        coverageStart = seekStart;
        windowEnd = seekStart;
        atEOF = false;
      }

      // Abort the fetch if the response stops delivering chunks. Re-armed on
      // every read so a slow-but-progressing extraction is never cut off.
      let stallTimer: ReturnType<typeof setTimeout> | null = null;
      const armStallTimer = () => {
        if (stallTimer !== null) clearTimeout(stallTimer);
        stallTimer = setTimeout(() => controller.abort(), FETCH_STALL_TIMEOUT_MS);
      };

      const url = appendPosition(activeUrl, seekStart);
      let succeeded = false;
      try {
        armStallTimer();
        const resp = await fetch(url, { signal: controller.signal });
        if (!resp.ok || !resp.body) {
          console.error(`[useSubtitleTracks] Failed to fetch ${url}: ${resp.status}`);
          return;
        }
        const reader = resp.body.getReader();
        const decoder = new TextDecoder();
        let buf = "";

        // Split on the last complete cue boundary (blank line) and parse
        // the safe prefix, keep the rest. The WebVTT muxer emits cues
        // terminated by "\n\n".
        while (!cancelled) {
          armStallTimer();
          const { value, done } = await reader.read();
          if (done) break;
          buf += decoder.decode(value, { stream: true }).replace(/\r\n/g, "\n");
          const split = buf.lastIndexOf("\n\n");
          if (split < 0) continue;
          const safe = buf.slice(0, split);
          buf = buf.slice(split + 2);
          const cues = parseVTT(safe);
          if (cues.length > 0) {
            addParsedCues(cues);
          }
        }

        // Flush any tail the muxer didn't terminate with a blank line.
        buf += decoder.decode();
        if (buf.trim()) {
          const cues = parseVTT(buf);
          if (cues.length > 0) {
            addParsedCues(cues);
          }
        }

        succeeded = true;
      } catch (err) {
        if ((err as Error).name !== "AbortError") {
          console.error("[useSubtitleTracks] Stream error:", err);
        }
      } finally {
        if (stallTimer !== null) clearTimeout(stallTimer);
        const superseded = inflight !== controller;
        if (inflight === controller) {
          inflight = null;
        }
        if (succeeded && !cancelled) {
          hasFetched = true;
          // Commit coverage only after the whole window streamed in. A
          // failed or stalled fetch must leave the range uncovered, or the
          // gap would read as fetched and never be retried — subtitles
          // would silently stop for the rest of the window.
          windowEnd = Math.max(windowEnd, requestedEnd);
          // End-of-input only when the window reaches the known media
          // duration. Don't infer it from where the window's cues stop:
          // a window that ends inside a dialogue gap looks identical to
          // end-of-file by that signal, and a false positive here silently
          // stops subtitles for the rest of playback.
          const durationSec = durationRef.current ?? 0;
          if (durationSec > 0 && requestedEnd >= durationSec) {
            atEOF = true;
          }
        } else if (!succeeded && !superseded && !cancelled) {
          // Genuine failure (error, stall, or non-ok response) rather than a
          // seek superseding this fetch — back off before retrying.
          lastFetchFailureAt = Date.now();
        }
      }
    }

    // Picks the right fetch action for the current player position:
    //   - no cues yet → initial fetch
    //   - current position fell behind coverageStart (backward seek
    //     outside window) → reset and fetch fresh window from here
    //   - position jumped past windowEnd (forward seek outside window) →
    //     reset and fetch fresh window from here, so the skipped-over gap
    //     is never mistaken for covered range
    //   - playback is nearing windowEnd and we haven't hit EOF → queue
    //     the next window, overlapping slightly with the previous
    function maybeFetch() {
      if (cancelled || inflight) return;
      if (Date.now() - lastFetchFailureAt < FETCH_RETRY_BACKOFF_MS) return;
      // Until the element has media loaded, currentTime reads 0 rather than
      // the position playback will actually start at (resume target, or a
      // seek that restarted the stream) — use the intended position instead.
      const mediaTime =
        videoEl.readyState > 0
          ? toMediaTime(videoEl.currentTime, streamOriginRef.current ?? 0)
          : (fetchAnchorRef.current ?? 0);
      if (!hasFetched) {
        fetchWindow(Math.max(0, mediaTime - SEEK_BACKOFF), true);
        return;
      }
      if (mediaTime < coverageStart - 1) {
        atEOF = false;
        fetchWindow(Math.max(0, mediaTime - SEEK_BACKOFF), true);
        return;
      }
      if (mediaTime > windowEnd + 1) {
        atEOF = false;
        fetchWindow(Math.max(0, mediaTime - SEEK_BACKOFF), true);
        return;
      }
      if (!atEOF && mediaTime > windowEnd - PREFETCH_LEAD) {
        const nextStart = Math.max(windowEnd - WINDOW_OVERLAP, mediaTime);
        fetchWindow(nextStart, false);
      }
    }

    // URL-backed tracks run the sliding-window fetcher; live tracks receive
    // their cues from the liveCues effect below instead.
    if (!activeIsLive) {
      // Kick off the first window before any player event fires so cues
      // are already in flight for the current position.
      maybeFetch();

      // Cue activation is driven by the browser via `cuechange`; these
      // listeners exist only to keep the sliding-window fetcher scheduled.
      videoEl.addEventListener("timeupdate", maybeFetch);
      videoEl.addEventListener("seeking", maybeFetch);
      videoEl.addEventListener("seeked", maybeFetch);
    }

    return () => {
      cancelled = true;
      inflight?.abort();
      inflight = null;
      videoEl.removeEventListener("timeupdate", maybeFetch);
      videoEl.removeEventListener("seeking", maybeFetch);
      videoEl.removeEventListener("seeked", maybeFetch);
      track.removeEventListener("cuechange", handleCueChange);
      // Snapshot loaded cues (converted back to source time) and coverage so
      // a rebuild against a reloaded <video> element can restore them without
      // refetching. Copy the dedup set: clearCues below empties the shared one.
      {
        const origin = appliedOriginRef.current;
        const delaySec = appliedDelayMsRef.current / 1000;
        carryoverRef.current = {
          url: activeUrl,
          cues: Array.from(track.cues ?? []).map((cue) => {
            const vc = cue as VTTCue;
            return {
              start: vc.startTime + origin - delaySec,
              end: vc.endTime + origin - delaySec,
              text: vc.text,
            };
          }),
          seen: new Set(seenCueKeysRef.current),
          coverageStart,
          windowEnd,
          atEOF,
          hasFetched,
        };
      }
      clearCues();
      // Tracks added via addTextTrack can't be removed from the element;
      // setting `disabled` makes it inert so a subsequent language change
      // cleanly creates a fresh track without stacking live listeners.
      track.mode = "disabled";
      if (trackRef.current === track) {
        trackRef.current = null;
      }
    };
    // `subtitleDelayMs` and `streamOriginSeconds` are intentionally excluded —
    // nudging delay or remapping the timeline must not tear down and refetch
    // the track. The update effects below shift existing cues in place instead.
    // `streamGeneration` IS included: a stream restart reloads the <video>
    // element and orphans the current track, so it must be rebuilt.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeUrl, activeCodec, activeLang, activeIsLive, liveTrackKey, streamGeneration, videoRef]);

  // Re-base already-loaded cues when the media timeline remaps — e.g. a
  // copy-mode session restarting at a new position after an out-of-window
  // seek changes `streamOriginSeconds`. Cue times are player-local
  // (`source - origin + delay`), so a larger origin moves every cue earlier.
  // Without this shift, cues loaded under the old origin display offset by
  // exactly the origin delta after the restart.
  useEffect(() => {
    const deltaSec = streamOriginSeconds - appliedOriginRef.current;
    appliedOriginRef.current = streamOriginSeconds;
    if (deltaSec === 0) return;
    const cues = trackRef.current?.cues;
    if (!cues) return;
    for (const cue of Array.from(cues)) {
      const vc = cue as VTTCue;
      vc.startTime = Math.max(0, vc.startTime - deltaSec);
      vc.endTime = vc.endTime - deltaSec;
    }
  }, [streamOriginSeconds]);

  // Apply delay changes to already-loaded cues without rebuilding the track.
  // Runs after the main effect, so trackRef is current.
  useEffect(() => {
    const track = trackRef.current;
    if (!track) {
      appliedDelayMsRef.current = subtitleDelayMs;
      return;
    }
    const deltaSec = (subtitleDelayMs - appliedDelayMsRef.current) / 1000;
    appliedDelayMsRef.current = subtitleDelayMs;
    if (deltaSec === 0) return;
    const cues = track.cues;
    if (!cues) return;
    for (const cue of Array.from(cues)) {
      const vc = cue as VTTCue;
      vc.startTime = Math.max(0, vc.startTime + deltaSec);
      vc.endTime = vc.endTime + deltaSec;
    }
  }, [subtitleDelayMs]);

  // Feed websocket-pushed cues into the active live track as they arrive. Only
  // the new tail since the last push is added (liveCues is append-only within a
  // job), so ingestion stays O(batch) rather than O(total) per push. When the
  // job restarts liveCues is replaced with a shorter array, which the length
  // check below detects to start over (the track itself is rebuilt via
  // liveTrackKey, so a fresh seen-set and pointer are already in place).
  useEffect(() => {
    if (!activeIsLive) return;
    const track = trackRef.current;
    if (!track || !liveCues) return;
    if (liveCues.length < processedLiveCuesRef.current) {
      processedLiveCuesRef.current = 0;
    }
    const fresh = liveCues.slice(processedLiveCuesRef.current);
    if (fresh.length === 0) return;
    processedLiveCuesRef.current = liveCues.length;
    const origin = appliedOriginRef.current;
    const delaySec = appliedDelayMsRef.current / 1000;
    addCuesToTrack(track, fresh, origin, delaySec, seenCueKeysRef.current);
    // While paused, adding a cue over the playhead doesn't reliably fire
    // `cuechange`, so refresh the on-screen text by hand. While playing the
    // browser drives `cuechange`, so skip the redundant state update.
    if (videoRef.current?.paused) {
      const active = track.activeCues;
      setActiveCueTexts(
        active && active.length > 0
          ? Array.from(active).map((c) => stripVTTTags((c as VTTCue).text))
          : [],
      );
    }
  }, [liveCues, activeIsLive, activeSubtitleIndex, liveTrackKey, videoRef]);

  return activeCueTexts;
}
