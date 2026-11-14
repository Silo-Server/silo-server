import type { PlayerV2Body } from "./player-v2";
import type { MarkerDraft, MarkerKind, PlayerMarkerProvenance, PlayerMarkerRange } from "./types";
import { provenanceOf } from "./utils/watchPageMarkers";

/** Preserve omission (unchanged), null (clear), and object (set) separately. */
export function markerUpdateToV2(
  update: Partial<Record<MarkerKind, { start?: number | null; end?: number | null } | null>>,
): PlayerV2Body<"PUT /api/v2/markers/files/{file_id}"> {
  const body: PlayerV2Body<"PUT /api/v2/markers/files/{file_id}"> = {};
  for (const kind of ["intro", "credits", "recap", "preview"] as const) {
    const segment = update[kind];
    if (segment === undefined) continue;
    body[kind] =
      segment === null
        ? null
        : {
            ...(segment.start != null ? { start_seconds: segment.start } : {}),
            ...(segment.end != null ? { end_seconds: segment.end } : {}),
          };
  }
  return body;
}

/** One kind as a marker write reports it: the stored range and its provenance. */
type SavedMarkerSegment = PlayerMarkerProvenance & {
  start_seconds?: number | null;
  end_seconds?: number | null;
};

/**
 * The markers to report to the player after a save: the draft's ranges with the
 * provenance the server recorded beside them.
 *
 * The response, not the draft, is the authority on where a range came from —
 * saving one is what makes it an edited range — and the skip prompt's source
 * line reads exactly that, so patching from the draft alone would leave the line
 * blank until the next watch read. The ranges stay the draft's: a kind the
 * viewer cleared is a clearance, which a response that omits it does not say.
 *
 * A kind's provenance is taken only when the response reports the draft's own
 * range. The response describes the stored row, and a kind the viewer did not
 * touch can be showing another range: an on-demand provider's marker, which is
 * never saved, or one a marker update moved while the editor was open. That
 * range goes back bare, so the player keeps the attribution it already holds
 * rather than taking the stored range's. An older server's response carries no
 * provenance, and a field it omits is absent here rather than invented.
 */
export function markerDraftFromV2(
  draft: MarkerDraft,
  saved: Partial<Record<MarkerKind, SavedMarkerSegment | null | undefined>>,
): MarkerDraft {
  const withProvenance = (kind: MarkerKind): PlayerMarkerRange | null => {
    const range = draft[kind];
    if (!range) return null;
    const segment = saved[kind];
    const bare = { start: range.start, end: range.end };
    const sameRange = segment?.start_seconds === range.start && segment?.end_seconds === range.end;
    return sameRange ? { ...bare, ...provenanceOf(segment) } : bare;
  };
  return {
    intro: withProvenance("intro"),
    recap: withProvenance("recap"),
    credits: withProvenance("credits"),
    preview: withProvenance("preview"),
  };
}
