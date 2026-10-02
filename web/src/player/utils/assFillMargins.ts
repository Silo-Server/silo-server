/**
 * Fill-mode support for ASS/SSA subtitles.
 *
 * In Fill mode the video and the JASSUB canvas share the same
 * `object-fit: cover` crop, which keeps positioned signs (`\pos`, `\move`)
 * attached to the picture. Regular events, however, are laid out against the
 * full encoded frame, so dialogue near an edge (commonly inside encoded black
 * bars) would be cropped away. libass solves this natively with negative
 * frame margins plus `ass_set_use_margins`, but JASSUB does not expose the
 * latter, so we emulate it: every style and explicit event margin grows by
 * the cropped amount, which moves regular events back inside the visible
 * area. Positioned events ignore margins, so signs stay aligned.
 */

/** Fraction of the rendered video hidden past each edge, per axis (0–0.5). */
export interface CoverCrop {
  x: number;
  y: number;
}

/** Margin growth, in script (PlayRes) units. */
export interface ASSMarginInset {
  horizontal: number;
  vertical: number;
}

export const NO_COVER_CROP: CoverCrop = { x: 0, y: 0 };
export const NO_ASS_MARGIN_INSET: ASSMarginInset = { horizontal: 0, vertical: 0 };

/**
 * Crop applied by `object-fit: cover` when a video of `videoAspect` fills a
 * `boxWidth` x `boxHeight` box, as a fraction of the rendered video per edge.
 */
export function computeCoverCrop(
  boxWidth: number,
  boxHeight: number,
  videoAspect: number,
): CoverCrop {
  if (!Number.isFinite(videoAspect) || videoAspect <= 0 || boxWidth <= 0 || boxHeight <= 0) {
    return NO_COVER_CROP;
  }
  const boxAspect = boxWidth / boxHeight;
  if (boxAspect > videoAspect) return { x: 0, y: (1 - videoAspect / boxAspect) / 2 };
  if (boxAspect < videoAspect) return { x: (1 - boxAspect / videoAspect) / 2, y: 0 };
  return NO_COVER_CROP;
}

/** How much larger than Fit the video renders under a given cover crop. */
export function coverZoom(crop: CoverCrop): number {
  const visible = 1 - 2 * Math.max(crop.x, crop.y);
  return visible > 0 ? 1 / visible : 1;
}

/**
 * Script resolution as libass resolves it, including its fallbacks when one
 * or both PlayRes headers are missing (libass `ass_lazy_track_init`).
 */
function resolvePlayRes(content: string): { x: number; y: number } {
  let x = 0;
  let y = 0;
  for (const line of content.split(/\r\n|\r|\n/)) {
    if (/^\s*\[(?!script info])[^\]]+]\s*$/i.test(line)) break;
    const match = line.match(/^PlayRes([XY]):\s*(-?\d+)/);
    if (!match) continue;
    if (match[1] === "X") x = Number.parseInt(match[2]!, 10);
    else y = Number.parseInt(match[2]!, 10);
  }
  if (x > 0 && y > 0) return { x, y };
  if (x <= 0 && y <= 0) return { x: 384, y: 288 };
  if (y <= 0) return { x, y: x === 1280 ? 1024 : Math.max(1, x - 1 - Math.floor((x - 1) / 4)) };
  return { x: y === 1024 ? 1280 : y + Math.floor(y / 3), y };
}

/** Converts a cover crop into margin growth for this script's PlayRes. */
export function resolveASSMarginInset(content: string, crop: CoverCrop): ASSMarginInset {
  if (crop.x <= 0 && crop.y <= 0) return NO_ASS_MARGIN_INSET;
  const playRes = resolvePlayRes(content);
  return {
    horizontal: Math.max(0, Math.round(crop.x * playRes.x)),
    vertical: Math.max(0, Math.round(crop.y * playRes.y)),
  };
}

export function sameASSMarginInset(a: ASSMarginInset, b: ASSMarginInset): boolean {
  return a.horizontal === b.horizontal && a.vertical === b.vertical;
}

function parseFormat(format: string): string[] {
  return format.split(",").map((field) => field.trim().toLowerCase());
}

// Field order libass assumes when a section has no Format line.
const DEFAULT_FORMATS: Record<string, string[]> = {
  "v4+ styles": parseFormat(
    "Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, " +
      "Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, " +
      "Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding",
  ),
  "v4 styles": parseFormat(
    "Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, TertiaryColour, BackColour, " +
      "Bold, Italic, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, " +
      "AlphaLevel, Encoding",
  ),
  events: parseFormat("Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text"),
};

/**
 * Grows style margins and explicit (non-zero) event margins by `inset`.
 * Event margins of 0 inherit the style's margin in libass, so they already
 * pick up the style change. Returns `content` unchanged for a zero inset.
 */
export function applyASSMarginInset(content: string, inset: ASSMarginInset): string {
  if (inset.horizontal <= 0 && inset.vertical <= 0) return content;

  const growth: Record<string, number> = {
    marginl: inset.horizontal,
    marginr: inset.horizontal,
    marginv: inset.vertical,
  };
  let section = "";
  let fields: string[] = [];

  return content
    .split(/(\r\n|\r|\n)/)
    .map((line) => {
      const header = line.match(/^\s*\[([^\]]+)]\s*$/);
      if (header) {
        section = header[1]!.trim().toLowerCase();
        fields = DEFAULT_FORMATS[section] ?? [];
        return line;
      }
      const isStyles = section === "v4+ styles" || section === "v4 styles";
      if (!isStyles && section !== "events") return line;

      const format = line.match(/^\s*Format\s*:\s*(.*)$/i);
      if (format) {
        fields = parseFormat(format[1]!);
        return line;
      }

      const entry = isStyles
        ? line.match(/^(\s*Style\s*:\s*)(.*)$/i)
        : line.match(/^(\s*Dialogue\s*:\s*)(.*)$/i);
      if (!entry || fields.length === 0) return line;

      // Only an event's final field (Text) may contain commas; style rows
      // have no free-text field, so every declared column is editable.
      const values = entry[2]!.split(",");
      if (values.length < fields.length) return line;
      const editable = isStyles ? fields.length : fields.length - 1;
      const head = values.slice(0, editable);
      const rest = values.slice(editable);

      for (const [name, amount] of Object.entries(growth)) {
        const index = fields.indexOf(name);
        if (amount <= 0 || index < 0 || index >= head.length) continue;
        const current = Number.parseInt(head[index]!.trim(), 10);
        if (!Number.isFinite(current)) continue;
        if (!isStyles && current === 0) continue;
        head[index] = String(current + amount);
      }
      const joined = isStyles ? [...head, ...rest] : [...head, rest.join(",")];
      return `${entry[1]}${joined.join(",")}`;
    })
    .join("");
}
