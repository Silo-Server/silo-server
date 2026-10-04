import { describe, expect, it } from "vitest";
import {
  decodePGSObject,
  mergePGSCompositions,
  pgsCompositionAt,
  pgsCompositionIndexAt,
  pgsPaletteEntryToRGBA,
  prunePGSTimeline,
  PGSStreamParser,
  type PGSComposition,
} from "./pgs";

function segment(type: number, ptsSeconds: number, body: number[]): number[] {
  const pts = Math.round(ptsSeconds * 90_000);
  return [
    0x50,
    0x47,
    (pts >>> 24) & 0xff,
    (pts >>> 16) & 0xff,
    (pts >>> 8) & 0xff,
    pts & 0xff,
    0,
    0,
    0,
    0,
    type,
    (body.length >> 8) & 0xff,
    body.length & 0xff,
    ...body,
  ];
}

const u16 = (value: number) => [(value >> 8) & 0xff, value & 0xff];

function pcs(
  pts: number,
  options: {
    state?: number;
    paletteId?: number;
    objects?: Array<{ id: number; x: number; y: number; forced?: boolean; crop?: number[] }>;
    width?: number;
    height?: number;
  } = {},
): number[] {
  const objects = options.objects ?? [];
  const body = [
    ...u16(options.width ?? 1920),
    ...u16(options.height ?? 1080),
    0x10,
    ...u16(1),
    options.state ?? 0x80,
    0,
    options.paletteId ?? 0,
    objects.length,
  ];
  for (const object of objects) {
    const flags = (object.crop ? 0x80 : 0) | (object.forced ? 0x40 : 0);
    body.push(...u16(object.id), 0, flags, ...u16(object.x), ...u16(object.y));
    if (object.crop) for (const value of object.crop) body.push(...u16(value));
  }
  return segment(0x16, pts, body);
}

function pds(pts: number, id: number, entries: Array<[number, number, number, number, number]>) {
  return segment(0x14, pts, [id, 0, ...entries.flat()]);
}

function ods(pts: number, id: number, width: number, height: number, rle: number[]): number[] {
  const length = rle.length + 4;
  return segment(0x15, pts, [
    ...u16(id),
    0,
    0xc0,
    (length >> 16) & 0xff,
    (length >> 8) & 0xff,
    length & 0xff,
    ...u16(width),
    ...u16(height),
    ...rle,
  ]);
}

const end = (pts: number) => segment(0x80, pts, []);

// Two lines of a 4x2 object: "1 1 2 2" then "0 0 0 1".
const RLE_4X2 = [1, 1, 0, 0x82, 2, 0, 0, 0, 0x03, 1, 0, 0];
const WHITE: [number, number, number, number, number] = [1, 235, 128, 128, 255];
const RED_HALF: [number, number, number, number, number] = [2, 63, 240, 102, 128];

function displaySetWithObject(pts: number): number[] {
  return [
    ...pcs(pts, { objects: [{ id: 0, x: 100, y: 900 }] }),
    ...pds(pts, 0, [WHITE, RED_HALF]),
    ...ods(pts, 0, 4, 2, RLE_4X2),
    ...end(pts),
  ];
}

describe("PGSStreamParser", () => {
  it("resolves a display set into a composition with its palette and object", () => {
    const parser = new PGSStreamParser();
    const [composition] = parser.push(new Uint8Array(displaySetWithObject(12.5)));

    expect(composition).toMatchObject({ start: 12.5, width: 1920, height: 1080 });
    expect(composition!.objects).toHaveLength(1);
    const object = composition!.objects[0]!;
    expect(object).toMatchObject({ x: 100, y: 900, width: 4, height: 2, crop: null });

    const pixels = decodePGSObject(object);
    const white = pgsPaletteEntryToRGBA(235, 128, 128, 255, 1080);
    const red = pgsPaletteEntryToRGBA(63, 240, 102, 128, 1080);
    expect(Array.from(pixels)).toEqual([white, white, red, red, 0, 0, 0, white]);
  });

  it("parses across arbitrary chunk boundaries", () => {
    const bytes = new Uint8Array([...displaySetWithObject(1), ...pcs(3, { state: 0 }), ...end(3)]);
    const parser = new PGSStreamParser();
    const compositions: PGSComposition[] = [];
    for (let offset = 0; offset < bytes.length; offset += 7) {
      compositions.push(...parser.push(bytes.subarray(offset, offset + 7)));
    }
    expect(compositions.map((c) => [c.start, c.objects.length])).toEqual([
      [1, 1],
      [3, 0],
    ]);
  });

  it("reuses objects and palettes defined earlier in the same epoch", () => {
    const parser = new PGSStreamParser();
    const compositions = parser.push(
      new Uint8Array([
        ...displaySetWithObject(1),
        // A later normal-state display set places object 0 again without
        // redefining it, with a crop.
        ...pcs(2, { state: 0, objects: [{ id: 0, x: 50, y: 60, crop: [1, 0, 2, 1] }] }),
        ...end(2),
      ]),
    );
    expect(compositions[1]!.objects[0]).toMatchObject({
      x: 50,
      y: 60,
      crop: { x: 1, y: 0, width: 2, height: 1 },
      width: 4,
      height: 2,
    });
  });

  it("forgets objects at an epoch start", () => {
    const parser = new PGSStreamParser();
    const compositions = parser.push(
      new Uint8Array([
        ...displaySetWithObject(1),
        ...pcs(2, { state: 0x80, objects: [{ id: 0, x: 0, y: 0 }] }),
        ...end(2),
      ]),
    );
    // The new epoch never defined object 0, so its screen cannot be drawn.
    expect(compositions.map((c) => c.start)).toEqual([1]);
  });

  it("keeps an earlier screen's colors after a palette-only update", () => {
    const parser = new PGSStreamParser();
    const [first, second] = parser.push(
      new Uint8Array([
        ...displaySetWithObject(1),
        ...pcs(2, { state: 0, objects: [{ id: 0, x: 100, y: 900 }] }),
        ...pds(2, 0, [[1, 235, 128, 128, 64]]),
        ...end(2),
      ]),
    );
    expect(first!.objects[0]!.palette[1]! >>> 24).toBe(255);
    expect(second!.objects[0]!.palette[1]! >>> 24).toBe(64);
  });

  it("assembles an object split across several segments", () => {
    const rle = RLE_4X2;
    const length = rle.length + 4;
    const firstPart = segment(0x15, 1, [
      0,
      0,
      0,
      0x80,
      (length >> 16) & 0xff,
      (length >> 8) & 0xff,
      length & 0xff,
      ...u16(4),
      ...u16(2),
      ...rle.slice(0, 5),
    ]);
    const lastPart = segment(0x15, 1, [0, 0, 0, 0x40, ...rle.slice(5)]);
    const parser = new PGSStreamParser();
    const [composition] = parser.push(
      new Uint8Array([
        ...pcs(1, { objects: [{ id: 0, x: 0, y: 0 }] }),
        ...pds(1, 0, [WHITE, RED_HALF]),
        ...firstPart,
        ...lastPart,
        ...end(1),
      ]),
    );
    expect(Array.from(composition!.objects[0]!.rle)).toEqual(rle);
  });

  it("drops a display set that places objects defined before the stream began", () => {
    // A window that starts mid-epoch: the screen's objects were defined in an
    // earlier window, so this parser cannot draw it and must not clear it.
    const parser = new PGSStreamParser();
    const compositions = parser.push(
      new Uint8Array([
        ...pcs(2, { state: 0, objects: [{ id: 0, x: 0, y: 0 }] }),
        ...end(2),
        ...pcs(3, { state: 0 }),
        ...end(3),
      ]),
    );
    expect(compositions.map((c) => [c.start, c.objects.length])).toEqual([[3, 0]]);
  });

  it("drops a display set whose palette was defined before the stream began", () => {
    const parser = new PGSStreamParser();
    const compositions = parser.push(
      new Uint8Array([
        ...pcs(2, { state: 0, objects: [{ id: 0, x: 0, y: 0 }] }),
        ...ods(2, 0, 4, 2, RLE_4X2),
        ...end(2),
      ]),
    );
    expect(compositions).toEqual([]);
  });

  it("keeps the epoch across windows when only the byte buffer is reset", () => {
    const parser = new PGSStreamParser();
    parser.push(new Uint8Array(displaySetWithObject(1)));
    // The first window ends partway through a segment header.
    parser.push(new Uint8Array([0x50, 0x47, 0, 0]));
    parser.resetBuffer();
    const [next] = parser.push(
      new Uint8Array([...pcs(5, { state: 0, objects: [{ id: 0, x: 10, y: 20 }] }), ...end(5)]),
    );
    expect(next).toMatchObject({ start: 5 });
    expect(next!.objects[0]).toMatchObject({ x: 10, y: 20, width: 4, height: 2 });
    expect(next!.objects[0]!.palette[1]! >>> 24).toBe(255);
  });

  it("skips garbage before the next segment", () => {
    const parser = new PGSStreamParser();
    const compositions = parser.push(new Uint8Array([1, 2, 0x50, 3, ...displaySetWithObject(4)]));
    expect(compositions.map((c) => c.start)).toEqual([4]);
  });
});

describe("decodePGSObject", () => {
  it("expands long runs and clips runs that overflow a line", () => {
    const palette = new Uint32Array(256);
    palette[5] = 0xff0000ff;
    const pixels = decodePGSObject({
      x: 0,
      y: 0,
      crop: null,
      forced: false,
      width: 3,
      height: 2,
      // 0x40-flagged 14-bit run of 300 transparent pixels, end of line, then
      // a 14-bit run of color 5.
      rle: new Uint8Array([0, 0x41, 0x2c, 0, 0, 0, 0xc0, 3, 5, 0, 0]),
      palette,
    });
    expect(Array.from(pixels)).toEqual([0, 0, 0, 0xff0000ff, 0xff0000ff, 0xff0000ff]);
  });
});

describe("PGS timeline helpers", () => {
  const at = (start: number, objects = 1): PGSComposition => ({
    start,
    width: 1920,
    height: 1080,
    objects: Array.from({ length: objects }, () => ({
      x: 0,
      y: 0,
      crop: null,
      forced: false,
      width: 1,
      height: 1,
      rle: new Uint8Array(),
      palette: new Uint32Array(256),
    })),
  });

  it("finds the composition on screen at a time", () => {
    const timeline = [at(1), at(2, 0), at(5)];
    expect(pgsCompositionIndexAt(timeline, 0.5)).toBe(-1);
    expect(pgsCompositionIndexAt(timeline, 1)).toBe(0);
    expect(pgsCompositionIndexAt(timeline, 4.9)).toBe(1);
    expect(pgsCompositionIndexAt(timeline, 9)).toBe(2);
  });

  it("keeps the copy an earlier window resolved when a window repeats a screen", () => {
    const earlier = at(1);
    const merged = mergePGSCompositions([earlier], [at(1, 2), at(3)]);
    expect(merged[0]).toBe(earlier);
    expect(merged.map((c) => c.start)).toEqual([1, 3]);
  });

  it("shows nothing past the loaded part of the timeline", () => {
    const timeline = [at(1), at(5)];
    expect(pgsCompositionAt(timeline, 9, 10)).toBe(timeline[1]);
    expect(pgsCompositionAt(timeline, 12, 10)).toBeNull();
    expect(pgsCompositionAt(timeline, 0.5, 10)).toBeNull();
  });

  it("prunes screens behind a point but keeps the one showing there", () => {
    const timeline = [at(1), at(2, 0), at(5), at(9)];
    expect(prunePGSTimeline(timeline, 6).map((c) => c.start)).toEqual([5, 9]);
    expect(prunePGSTimeline(timeline, 0.5)).toBe(timeline);
  });

  it("merges overlapping windows without letting an unresolved copy erase a screen", () => {
    const merged = mergePGSCompositions([at(1), at(2, 0)], [at(1, 0), at(3)]);
    expect(merged.map((c) => [c.start, c.objects.length])).toEqual([
      [1, 1],
      [2, 0],
      [3, 1],
    ]);
  });
});
