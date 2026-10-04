/**
 * Parser for HDMV Presentation Graphic Stream (PGS, Blu-ray `.sup`) subtitles.
 *
 * A `.sup` stream is a run of segments, each `"PG" | PTS | DTS | type | size`.
 * Segments group into display sets: a presentation composition (PCS), optional
 * window (WDS), palette (PDS) and object (ODS) definitions, then END. Palettes
 * and objects persist across display sets until the next epoch start, so a
 * later display set may place an object defined earlier. Each display set
 * resolves here to a {@link PGSComposition}: everything needed to draw that
 * screen without consulting earlier state, or an empty object list to clear.
 * A display set that places an object or palette this parser never saw (its
 * epoch began before the stream did) is dropped rather than turned into a
 * clear, so it cannot erase a screen that is still showing.
 *
 * Bitmaps stay run-length encoded until drawn ({@link decodePGSObject}); a
 * feature-length track holds thousands of display sets and only the one on
 * screen needs pixels.
 */

const SEGMENT_HEADER_BYTES = 13;
const PGS_TIMESCALE = 90_000;

const SEGMENT_PDS = 0x14;
const SEGMENT_ODS = 0x15;
const SEGMENT_PCS = 0x16;
const SEGMENT_WDS = 0x17;
const SEGMENT_END = 0x80;

const COMPOSITION_EPOCH_START = 0x80;
const ODS_FIRST_IN_SEQUENCE = 0x80;
const ODS_LAST_IN_SEQUENCE = 0x40;
const OBJECT_CROPPED = 0x80;
const OBJECT_FORCED = 0x40;

export interface PGSCrop {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface PGSPlacedObject {
  /** Top-left of the (cropped) object on the composition plane. */
  x: number;
  y: number;
  /** Source rectangle within the object bitmap, or null for the whole object. */
  crop: PGSCrop | null;
  forced: boolean;
  width: number;
  height: number;
  rle: Uint8Array;
  /** 256 entries of packed RGBA (`r | g << 8 | b << 16 | a << 24`). */
  palette: Uint32Array;
}

export interface PGSComposition {
  /** Source time, in seconds, at which this screen replaces the previous one. */
  start: number;
  /** Size of the plane the object coordinates refer to. */
  width: number;
  height: number;
  /** Empty when the display set clears the screen. */
  objects: PGSPlacedObject[];
}

interface PendingObject {
  id: number;
  width: number;
  height: number;
  expectedBytes: number;
  chunks: Uint8Array[];
  receivedBytes: number;
}

interface ObjectDefinition {
  width: number;
  height: number;
  rle: Uint8Array;
}

interface CompositionReference {
  objectId: number;
  x: number;
  y: number;
  crop: PGSCrop | null;
  forced: boolean;
}

interface PresentationSegment {
  pts: number;
  width: number;
  height: number;
  paletteId: number;
  objects: CompositionReference[];
}

function clampByte(value: number): number {
  return value < 0 ? 0 : value > 255 ? 255 : Math.round(value);
}

/**
 * Converts one limited-range palette entry to packed RGBA. Blu-ray HD streams
 * use BT.709 coefficients; SD planes use BT.601, matching FFmpeg's decoder.
 */
export function pgsPaletteEntryToRGBA(
  y: number,
  cr: number,
  cb: number,
  alpha: number,
  planeHeight: number,
): number {
  const luma = 1.164 * (y - 16);
  const r = planeHeight > 576 ? luma + 1.793 * (cr - 128) : luma + 1.596 * (cr - 128);
  const g =
    planeHeight > 576
      ? luma - 0.213 * (cb - 128) - 0.533 * (cr - 128)
      : luma - 0.392 * (cb - 128) - 0.813 * (cr - 128);
  const b = planeHeight > 576 ? luma + 2.112 * (cb - 128) : luma + 2.017 * (cb - 128);
  return (clampByte(r) | (clampByte(g) << 8) | (clampByte(b) << 16) | ((alpha & 0xff) << 24)) >>> 0;
}

/**
 * Expands an object's run-length encoded bitmap into packed RGBA pixels.
 * Malformed runs are clipped to the object rather than rejected: a damaged
 * line should cost that line, not the whole subtitle.
 */
export function decodePGSObject(object: PGSPlacedObject): Uint32Array<ArrayBuffer> {
  const { width, height, rle, palette } = object;
  const pixels = new Uint32Array(width * height);
  let x = 0;
  let y = 0;
  let i = 0;
  const put = (color: number, count: number) => {
    if (y >= height) return;
    const end = Math.min(width, x + count);
    if (end > x) pixels.fill(palette[color] ?? 0, y * width + x, y * width + end);
    x += count;
  };
  while (i < rle.length && y < height) {
    const first = rle[i++]!;
    if (first !== 0) {
      put(first, 1);
      continue;
    }
    if (i >= rle.length) break;
    const flags = rle[i++]!;
    if (flags === 0) {
      x = 0;
      y += 1;
      continue;
    }
    let length = flags & 0x3f;
    if (flags & 0x40) {
      if (i >= rle.length) break;
      length = (length << 8) | rle[i++]!;
    }
    let color = 0;
    if (flags & 0x80) {
      if (i >= rle.length) break;
      color = rle[i++]!;
    }
    put(color, length);
  }
  return pixels;
}

function concat(chunks: Uint8Array[], total: number): Uint8Array {
  // Always a copy: a view into the network chunk would keep the whole chunk
  // alive for as long as the timeline holds this object.
  if (chunks.length === 1 && chunks[0]!.byteLength === total) return chunks[0]!.slice();
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk.subarray(0, Math.min(chunk.byteLength, total - offset)), offset);
    offset += chunk.byteLength;
    if (offset >= total) break;
  }
  return out;
}

/**
 * Incremental `.sup` parser. Feed it bytes as they arrive with {@link push};
 * it returns the compositions completed by that chunk, in stream order.
 *
 * Bytes that do not start with the `PG` magic are skipped one at a time until
 * the next segment boundary, so a window that starts mid-segment or a damaged
 * stretch costs only the affected display set.
 */
export class PGSStreamParser {
  private buffer = new Uint8Array(0);
  private palettes = new Map<number, Uint32Array>();
  private objects = new Map<number, ObjectDefinition>();
  private pendingObject: PendingObject | null = null;
  private presentation: PresentationSegment | null = null;
  private received = -Infinity;

  /**
   * Source time, in seconds, of the latest complete segment read. Segments
   * arrive in time order, so nothing before this point is still to come.
   */
  get receivedUntil(): number {
    return this.received;
  }

  /**
   * Discards partial input before the stream continues from another window.
   * Palettes and objects stay, so a window that resumes an epoch can still
   * draw screens that place objects defined in the window before it.
   */
  resetBuffer(): void {
    this.buffer = new Uint8Array(0);
    this.pendingObject = null;
    this.presentation = null;
  }

  push(chunk: Uint8Array): PGSComposition[] {
    if (chunk.byteLength > 0) {
      const merged = new Uint8Array(this.buffer.byteLength + chunk.byteLength);
      merged.set(this.buffer, 0);
      merged.set(chunk, this.buffer.byteLength);
      this.buffer = merged;
    }
    const completed: PGSComposition[] = [];
    let offset = 0;
    const bytes = this.buffer;
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    while (offset + SEGMENT_HEADER_BYTES <= bytes.byteLength) {
      if (bytes[offset] !== 0x50 || bytes[offset + 1] !== 0x47) {
        offset += 1;
        continue;
      }
      const pts = view.getUint32(offset + 2);
      const type = bytes[offset + 10]!;
      const size = view.getUint16(offset + 11);
      const end = offset + SEGMENT_HEADER_BYTES + size;
      if (end > bytes.byteLength) break;
      const body = bytes.subarray(offset + SEGMENT_HEADER_BYTES, end);
      this.received = Math.max(this.received, pts / PGS_TIMESCALE);
      const composition = this.handleSegment(type, pts, body);
      if (composition) completed.push(composition);
      offset = end;
    }
    this.buffer = bytes.slice(offset);
    return completed;
  }

  private handleSegment(type: number, pts: number, body: Uint8Array): PGSComposition | null {
    switch (type) {
      case SEGMENT_PCS:
        this.readPresentation(pts, body);
        return null;
      case SEGMENT_PDS:
        this.readPalette(body);
        return null;
      case SEGMENT_ODS:
        this.readObject(body);
        return null;
      case SEGMENT_WDS:
        // Windows bound where objects may draw; placements already carry their
        // own coordinates, so nothing here changes what is rendered.
        return null;
      case SEGMENT_END:
        return this.finishDisplaySet();
      default:
        return null;
    }
  }

  private readPresentation(pts: number, body: Uint8Array): void {
    if (body.byteLength < 11) {
      this.presentation = null;
      return;
    }
    const view = new DataView(body.buffer, body.byteOffset, body.byteLength);
    const width = view.getUint16(0);
    const height = view.getUint16(2);
    const state = body[7]!;
    const paletteId = body[9]!;
    const count = body[10]!;
    if (state & COMPOSITION_EPOCH_START) {
      this.palettes.clear();
      this.objects.clear();
      this.pendingObject = null;
    }
    const objects: CompositionReference[] = [];
    let offset = 11;
    for (let n = 0; n < count && offset + 8 <= body.byteLength; n++) {
      const objectId = view.getUint16(offset);
      const flags = body[offset + 3]!;
      const x = view.getUint16(offset + 4);
      const y = view.getUint16(offset + 6);
      offset += 8;
      let crop: PGSCrop | null = null;
      if (flags & OBJECT_CROPPED) {
        if (offset + 8 > body.byteLength) break;
        crop = {
          x: view.getUint16(offset),
          y: view.getUint16(offset + 2),
          width: view.getUint16(offset + 4),
          height: view.getUint16(offset + 6),
        };
        offset += 8;
      }
      objects.push({ objectId, x, y, crop, forced: (flags & OBJECT_FORCED) !== 0 });
    }
    this.presentation = { pts, width, height, paletteId, objects };
  }

  private readPalette(body: Uint8Array): void {
    if (body.byteLength < 2) return;
    const id = body[0]!;
    const planeHeight = this.presentation?.height ?? 1080;
    const existing = this.palettes.get(id);
    const palette = existing ? new Uint32Array(existing) : new Uint32Array(256);
    for (let offset = 2; offset + 5 <= body.byteLength; offset += 5) {
      const entry = body[offset]!;
      palette[entry] = pgsPaletteEntryToRGBA(
        body[offset + 1]!,
        body[offset + 2]!,
        body[offset + 3]!,
        body[offset + 4]!,
        planeHeight,
      );
    }
    this.palettes.set(id, palette);
  }

  private readObject(body: Uint8Array): void {
    if (body.byteLength < 4) return;
    const view = new DataView(body.buffer, body.byteOffset, body.byteLength);
    const id = view.getUint16(0);
    const sequence = body[3]!;
    let data: Uint8Array;
    if (sequence & ODS_FIRST_IN_SEQUENCE) {
      if (body.byteLength < 11) return;
      // The 24-bit length counts the width and height fields that follow it.
      const expectedBytes = Math.max(0, ((body[4]! << 16) | (body[5]! << 8) | body[6]!) - 4);
      this.pendingObject = {
        id,
        width: view.getUint16(7),
        height: view.getUint16(9),
        expectedBytes,
        chunks: [],
        receivedBytes: 0,
      };
      data = body.subarray(11);
    } else {
      if (!this.pendingObject || this.pendingObject.id !== id) return;
      data = body.subarray(4);
    }
    const pending = this.pendingObject;
    if (!pending) return;
    pending.chunks.push(data);
    pending.receivedBytes += data.byteLength;
    if (sequence & ODS_LAST_IN_SEQUENCE || pending.receivedBytes >= pending.expectedBytes) {
      const total = Math.min(pending.receivedBytes, pending.expectedBytes || pending.receivedBytes);
      this.objects.set(id, {
        width: pending.width,
        height: pending.height,
        rle: concat(pending.chunks, total),
      });
      this.pendingObject = null;
    }
  }

  private finishDisplaySet(): PGSComposition | null {
    const presentation = this.presentation;
    this.presentation = null;
    if (!presentation) return null;
    const palette = this.palettes.get(presentation.paletteId);
    if (!palette && presentation.objects.length > 0) return null;
    const objects: PGSPlacedObject[] = [];
    for (const reference of presentation.objects) {
      const definition = this.objects.get(reference.objectId);
      if (!definition) return null;
      if (definition.width === 0 || definition.height === 0) continue;
      objects.push({
        x: reference.x,
        y: reference.y,
        crop: reference.crop,
        forced: reference.forced,
        width: definition.width,
        height: definition.height,
        rle: definition.rle,
        // readPalette copies on write, so a later palette-only update cannot
        // recolor this screen.
        palette: palette!,
      });
    }
    return {
      start: presentation.pts / PGS_TIMESCALE,
      width: presentation.width,
      height: presentation.height,
      objects,
    };
  }
}

/**
 * Index of the composition on screen at `time`: the last one starting at or
 * before it. Returns -1 before the first composition.
 */
export function pgsCompositionIndexAt(
  compositions: readonly PGSComposition[],
  time: number,
): number {
  let low = 0;
  let high = compositions.length - 1;
  let found = -1;
  while (low <= high) {
    const mid = (low + high) >> 1;
    if (compositions[mid]!.start <= time) {
      found = mid;
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }
  return found;
}

/**
 * The composition on screen at `time`, or null before the first one and past
 * `loadedUntil`: the clear that ends the last loaded screen may sit in a
 * window that has not arrived, so that screen must not stay up indefinitely.
 */
export function pgsCompositionAt(
  compositions: readonly PGSComposition[],
  time: number,
  loadedUntil: number,
): PGSComposition | null {
  if (time > loadedUntil) return null;
  const index = pgsCompositionIndexAt(compositions, time);
  return index >= 0 ? compositions[index]! : null;
}

/**
 * Drops compositions that ended before `keepFrom`, keeping the one still on
 * screen there, so a long track does not hold every screen it has shown.
 */
export function prunePGSTimeline(timeline: PGSComposition[], keepFrom: number): PGSComposition[] {
  const index = pgsCompositionIndexAt(timeline, keepFrom);
  return index > 0 ? timeline.slice(index) : timeline;
}

/**
 * Merges newly parsed compositions into a start-ordered timeline, dropping
 * repeats. Overlapping fetch windows deliver the same display sets twice; a
 * repeat starts at the same instant, and the copy already in the timeline
 * wins because its window parsed the epoch in order.
 */
export function mergePGSCompositions(
  timeline: PGSComposition[],
  incoming: readonly PGSComposition[],
): PGSComposition[] {
  if (incoming.length === 0) return timeline;
  const byStart = new Map<number, PGSComposition>();
  for (const composition of timeline) byStart.set(composition.start, composition);
  for (const composition of incoming) {
    if (!byStart.has(composition.start)) byStart.set(composition.start, composition);
  }
  return Array.from(byStart.values()).sort((a, b) => a.start - b.start);
}
