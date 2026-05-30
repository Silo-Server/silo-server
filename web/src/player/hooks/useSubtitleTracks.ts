import { useEffect, useRef, useState } from "react";
import { parseVTT, type ParsedCue } from "../utils/parseVTT";
import type { PlayerSubtitleInfo } from "../types";
import { isASSCodec } from "../utils/assSubtitles";
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
    const key = `${startTime}|${endTime}|${parsed.text}`;
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
 */
export function useSubtitleTracks(
  videoRef: React.RefObject<HTMLVideoElement | null>,
  subtitleUrls: PlayerSubtitleInfo[],
  activeSubtitleIndex: number | null,
  streamOriginRef: React.RefObject<number>,
  subtitleDelayMs: number,
  liveCues?: ParsedCue[] | null,
  // Identifies the current live translation job. Changing it (a new job) rebuilds
  // the track and resets the dedup set so cues from a prior run never linger.
  liveTrackKey?: string | null,
): string[] {
  const [activeCueTexts, setActiveCueTexts] = useState<string[]>([]);

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

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    const videoEl: HTMLVideoElement = video;

    setActiveCueTexts([]);

    // Skip entirely for ASS/SSA: JASSUB handles those via useASSSubtitles.
    if (isASSCodec(activeCodec)) {
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

    let cancelled = false;
    let hasFetched = false;

    // Sliding-window coverage state. See fetchWindow for semantics.
    let coverageStart = 0;
    let windowEnd = 0;
    let atEOF = false;
    let inflight: AbortController | null = null;

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
      // subtraction is a no-op. Any active user-facing sync delay gets
      // baked in here so new cues line up with existing ones.
      const origin = streamOriginRef.current ?? 0;
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
        windowEnd = requestedEnd;
        atEOF = false;
      } else {
        windowEnd = Math.max(windowEnd, requestedEnd);
      }

      const url = appendPosition(activeUrl, seekStart);
      try {
        const resp = await fetch(url, { signal: controller.signal });
        if (!resp.ok || !resp.body) {
          console.error(`[useSubtitleTracks] Failed to fetch ${url}: ${resp.status}`);
          return;
        }
        const reader = resp.body.getReader();
        const decoder = new TextDecoder();
        let buf = "";
        let lastCueEnd = 0;

        // Split on the last complete cue boundary (blank line) and parse
        // the safe prefix, keep the rest. The WebVTT muxer emits cues
        // terminated by "\n\n".
        while (!cancelled) {
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
            lastCueEnd = cues[cues.length - 1]!.end;
          }
        }

        // Flush any tail the muxer didn't terminate with a blank line.
        buf += decoder.decode();
        if (buf.trim()) {
          const cues = parseVTT(buf);
          if (cues.length > 0) {
            addParsedCues(cues);
            lastCueEnd = cues[cues.length - 1]!.end;
          }
        }

        // If ffmpeg closed well short of the requested end, treat it as
        // end-of-input and stop prefetching.
        if (lastCueEnd > 0 && lastCueEnd < requestedEnd - WINDOW_OVERLAP) {
          atEOF = true;
        }
      } catch (err) {
        if ((err as Error).name !== "AbortError") {
          console.error("[useSubtitleTracks] Stream error:", err);
        }
      } finally {
        if (inflight === controller) {
          inflight = null;
        }
        if (!controller.signal.aborted) {
          hasFetched = true;
        }
      }
    }

    // Picks the right fetch action for the current player position:
    //   - no cues yet → initial fetch
    //   - current position fell behind coverageStart (backward seek
    //     outside window) → reset and fetch fresh window from here
    //   - playback is nearing windowEnd and we haven't hit EOF → queue
    //     the next window, overlapping slightly with the previous
    function maybeFetch() {
      if (cancelled || inflight) return;
      const mediaTime = toMediaTime(videoEl.currentTime, streamOriginRef.current ?? 0);
      if (!hasFetched) {
        fetchWindow(Math.max(0, mediaTime - SEEK_BACKOFF), true);
        return;
      }
      if (mediaTime < coverageStart - 1) {
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
      clearCues();
      // Tracks added via addTextTrack can't be removed from the element;
      // setting `disabled` makes it inert so a subsequent language change
      // cleanly creates a fresh track without stacking live listeners.
      track.mode = "disabled";
      if (trackRef.current === track) {
        trackRef.current = null;
      }
    };
    // `subtitleDelayMs` is intentionally excluded — nudging delay must not
    // tear down and refetch the track. The delay-update effect below shifts
    // existing cues in place instead.
  }, [activeUrl, activeCodec, activeLang, activeIsLive, liveTrackKey, streamOriginRef, videoRef]);

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
    const origin = streamOriginRef.current ?? 0;
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
  }, [liveCues, activeIsLive, activeSubtitleIndex, liveTrackKey, streamOriginRef, videoRef]);

  return activeCueTexts;
}
