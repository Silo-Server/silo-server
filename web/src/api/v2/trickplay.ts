import type { components } from "@/api/v2/schema";
import type { PlayerTrickplay } from "@/player/trickplay";

type WatchTrickplayV2 = components["schemas"]["WatchTrickplay"];

/** Adapts a v2 trickplay manifest to the player's shape. */
export function trickplayFromV2(manifest: WatchTrickplayV2): PlayerTrickplay {
  const sheets = [...manifest.sheets].sort((a, b) => a.index - b.index).map((sheet) => sheet.url);
  return {
    intervalMs: manifest.interval_ms,
    width: manifest.thumbnail_width,
    height: manifest.thumbnail_height,
    columns: manifest.tile_columns,
    rows: manifest.tile_rows,
    count: manifest.thumbnail_count,
    sheets,
    expiresAt: Date.parse(manifest.expires_at),
  };
}
