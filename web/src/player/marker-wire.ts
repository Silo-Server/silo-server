import type { PlayerV2Body } from "./player-v2";
import type { MarkerDraft, MarkerKind, PlayerMarkerRange } from "./types";

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

/** One kind's provenance as a marker write reports it. */
type SavedMarkerProvenance = {
  source?: string | null;
  provider?: string | null;
  confidence?: number | null;
  algorithm?: string | null;
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
 * An older server's response carries no provenance, and a field it omits is
 * absent here rather than invented.
 */
export function markerDraftFromV2(
  draft: MarkerDraft,
  saved: Partial<Record<MarkerKind, SavedMarkerProvenance | null | undefined>>,
): MarkerDraft {
  const withProvenance = (kind: MarkerKind): PlayerMarkerRange | null => {
    const range = draft[kind];
    if (!range) return null;
    const segment = saved[kind];
    return {
      start: range.start,
      end: range.end,
      ...(segment?.source ? { source: segment.source } : {}),
      ...(segment?.provider ? { provider: segment.provider } : {}),
      ...(segment?.confidence != null ? { confidence: segment.confidence } : {}),
      ...(segment?.algorithm ? { algorithm: segment.algorithm } : {}),
    };
  };
  return {
    intro: withProvenance("intro"),
    recap: withProvenance("recap"),
    credits: withProvenance("credits"),
    preview: withProvenance("preview"),
  };
}
