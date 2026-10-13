/**
 * Seek-bar previews (trickplay): sprite sheets of thumbnails, one thumbnail
 * per interval of media time. Thumbnail i shows what plays from
 * i*intervalMs to (i+1)*intervalMs and sits on sheet
 * floor(i / (columns*rows)), left to right and top to bottom. Every sheet
 * has the full grid.
 */
export interface PlayerTrickplay {
  intervalMs: number;
  width: number;
  height: number;
  columns: number;
  rows: number;
  count: number;
  /** One URL per sheet, in order. */
  sheets: string[];
  /** When the sheet URLs stop working, in epoch milliseconds. */
  expiresAt: number;
}

export interface TrickplayTile {
  sheet: number;
  url: string;
  /** Pixel offset of the thumbnail within its sheet, at full size. */
  x: number;
  y: number;
}

/** The thumbnail shown for a media time, or null outside the previews. */
export function trickplayTile(trickplay: PlayerTrickplay, seconds: number): TrickplayTile | null {
  if (!Number.isFinite(seconds) || trickplay.count <= 0 || trickplay.intervalMs <= 0) {
    return null;
  }
  const index = Math.min(
    Math.max(0, Math.floor((seconds * 1000) / trickplay.intervalMs)),
    trickplay.count - 1,
  );
  const perSheet = trickplay.columns * trickplay.rows;
  const sheet = Math.floor(index / perSheet);
  const url = trickplay.sheets[sheet];
  if (!url) return null;
  const cell = index % perSheet;
  return {
    sheet,
    url,
    x: (cell % trickplay.columns) * trickplay.width,
    y: Math.floor(cell / trickplay.columns) * trickplay.height,
  };
}
