import type { PlayerV2Body } from "./player-v2";
import type { MarkerKind } from "./types";

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
