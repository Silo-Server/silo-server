import type { PlanV3, TrackIdentityV3 } from "../protocol-v3";

/**
 * Whether `next` streams exactly what `current` already streams, so the
 * player can take it without opening its media again.
 *
 * Only the original file served as it is (`original_http`) qualifies. Its
 * address names the playback session, and the stream token in its query
 * carries the session, the file and the audio track, so the address the
 * player opened keeps serving the same bytes. A subtitle the player draws
 * over the video is not part of that stream; a burned-in one is.
 *
 * Everything the stream is built from has to match: the session, the file,
 * the addresses (a replan signs a new token into the query, so only paths are
 * compared), the headers and how they are refreshed, the audio track, the
 * recipe and how the timeline is anchored. Anything else, such as a proxy
 * address with the token in its path, counts as a new stream.
 */
export function keepsOriginalStream(current: PlanV3, next: PlanV3): boolean {
  return (
    current.delivery === "original_http" &&
    next.delivery === "original_http" &&
    next.subtitle.mode !== "burn_in" &&
    current.session_id !== undefined &&
    current.session_id === next.session_id &&
    current.effective_media_file_id === next.effective_media_file_id &&
    current.stream.protocol === next.stream.protocol &&
    current.stream.container === next.stream.container &&
    current.stream.mime_type === next.stream.mime_type &&
    current.stream.header_refresh === next.stream.header_refresh &&
    addressPath(current.stream.header_refresh_url) ===
      addressPath(next.stream.header_refresh_url) &&
    addressPath(current.stream.url) === addressPath(next.stream.url) &&
    addressPath(current.stream.parallel_range_url) ===
      addressPath(next.stream.parallel_range_url) &&
    sameEntries(current.stream.headers, next.stream.headers) &&
    sameTrack(current.selected_tracks.audio, next.selected_tracks.audio) &&
    sameEntries(current.effective_recipe, next.effective_recipe) &&
    current.timeline.stream_origin_seconds === next.timeline.stream_origin_seconds &&
    current.timeline.timeline_offset_seconds === next.timeline.timeline_offset_seconds &&
    current.timeline.can_seek_anywhere === next.timeline.can_seek_anywhere
  );
}

function addressPath(url: string | undefined): string | undefined {
  return url?.split("?")[0];
}

function sameTrack(a: TrackIdentityV3 | undefined, b: TrackIdentityV3 | undefined): boolean {
  return a?.id === b?.id && a?.index === b?.index;
}

function sameEntries(a: object | undefined, b: object | undefined): boolean {
  const left = Object.entries(a ?? {}).filter(([, value]) => value !== undefined);
  const right = Object.entries(b ?? {}).filter(([, value]) => value !== undefined);
  return (
    left.length === right.length &&
    left.every(([key, value]) => (b as Record<string, unknown>)[key] === value)
  );
}
