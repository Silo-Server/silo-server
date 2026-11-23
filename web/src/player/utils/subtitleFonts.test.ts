// @vitest-environment node

import { describe, expect, it } from "vitest";
import { fontFamilyNames, forceASSFontFamily } from "./subtitleFonts";

const SCRIPT = [
  "[Script Info]",
  "PlayResY: 1080",
  "",
  "[V4+ Styles]",
  "Format: Name, Fontname, Fontsize, Bold",
  "Style: Default,Adobe Arabic,80,-1",
  "Style: Sign,Trebuchet MS,60,0",
  "Style: Vertical,@Adobe Arabic,60,0",
  "",
  "[Events]",
  "Format: Layer, Start, End, Style, Text",
  "Dialogue: 0,0:00:01.00,0:00:02.00,Default,{\\fnAdobe Arabic}attached",
  "Dialogue: 0,0:00:01.00,0:00:02.00,Default,{\\fnArial}missing",
  "Dialogue: 0,0:00:01.00,0:00:02.00,Default,{\\fnArial}sign{\\fn}back to the style font{\\fnArial}sign{\\fn0}again",
].join("\n");

describe("forceASSFontFamily", () => {
  it("forces every style and \\fn override when no fonts are attached", () => {
    expect(forceASSFontFamily(SCRIPT, "noto sans arabic")).toContain(
      [
        "Style: Default,noto sans arabic,80,-1",
        "Style: Sign,noto sans arabic,60,0",
        "Style: Vertical,noto sans arabic,60,0",
      ].join("\n"),
    );
  });

  it("keeps styles and \\fn overrides that name an attached font", () => {
    const out = forceASSFontFamily(SCRIPT, "noto sans arabic", new Set(["adobe arabic"]));

    expect(out).toContain(
      [
        "Style: Default,Adobe Arabic,80,-1",
        "Style: Sign,noto sans arabic,60,0",
        "Style: Vertical,@Adobe Arabic,60,0",
      ].join("\n"),
    );
    expect(out).toContain("{\\fnAdobe Arabic}attached");
    expect(out).toContain("{\\fnnoto sans arabic}missing");
    expect(out).toContain(
      "{\\fnnoto sans arabic}sign{\\fn}back to the style font{\\fnnoto sans arabic}sign{\\fn0}again",
    );
  });
});

type NameRecord = { platform: number; encoding: number; nameId: number; value: string };

/** A bare sfnt holding only a `name` table, laid out per the OpenType spec. */
function sfntWithNames(records: NameRecord[]): Uint8Array {
  const strings = records.map(({ platform, value }) =>
    platform === 1
      ? Uint8Array.from(value, (c) => c.charCodeAt(0))
      : Uint8Array.from([...value].flatMap((c) => [c.charCodeAt(0) >> 8, c.charCodeAt(0) & 0xff])),
  );
  const storageOffset = 6 + records.length * 12;
  const nameLength = storageOffset + strings.reduce((n, s) => n + s.length, 0);
  const tableOffset = 12 + 16;
  const out = new Uint8Array(tableOffset + nameLength);
  const view = new DataView(out.buffer);
  view.setUint32(0, 0x00010000);
  view.setUint16(4, 1);
  out.set(
    [..."name"].map((c) => c.charCodeAt(0)),
    12,
  );
  view.setUint32(20, tableOffset);
  view.setUint32(24, nameLength);

  view.setUint16(tableOffset + 2, records.length);
  view.setUint16(tableOffset + 4, storageOffset);
  let stringOffset = 0;
  records.forEach((record, i) => {
    const at = tableOffset + 6 + i * 12;
    view.setUint16(at, record.platform);
    view.setUint16(at + 2, record.encoding);
    view.setUint16(at + 4, record.platform === 1 ? 0 : 0x409);
    view.setUint16(at + 6, record.nameId);
    view.setUint16(at + 8, strings[i]!.length);
    view.setUint16(at + 10, stringOffset);
    out.set(strings[i]!, tableOffset + storageOffset + stringOffset);
    stringOffset += strings[i]!.length;
  });
  return out;
}

describe("fontFamilyNames", () => {
  const font = sfntWithNames([
    { platform: 3, encoding: 1, nameId: 1, value: "Adobe Arabic" },
    { platform: 3, encoding: 1, nameId: 2, value: "Bold" },
    { platform: 3, encoding: 1, nameId: 4, value: "Adobe Arabic Bold" },
    { platform: 1, encoding: 0, nameId: 6, value: "AdobeArabic-Bold" },
  ]);

  it("reads the family, full and PostScript names libass matches styles against", () => {
    expect(fontFamilyNames(font).sort()).toEqual([
      "adobe arabic",
      "adobe arabic bold",
      "adobearabic-bold",
    ]);
  });

  it("reads every font of a TrueType collection", () => {
    const second = sfntWithNames([{ platform: 3, encoding: 1, nameId: 1, value: "Other Face" }]);
    // ttcf header: tag, version, numFonts, then one offset per font.
    const firstAt = 20;
    const secondAt = firstAt + font.length;
    const collection = new Uint8Array(secondAt + second.length);
    const view = new DataView(collection.buffer);
    collection.set([..."ttcf"].map((c) => c.charCodeAt(0)));
    view.setUint32(4, 0x00010000);
    view.setUint32(8, 2);
    view.setUint32(12, firstAt);
    view.setUint32(16, secondAt);
    collection.set(relocate(font, firstAt), firstAt);
    collection.set(relocate(second, secondAt), secondAt);

    expect(fontFamilyNames(collection).sort()).toEqual([
      "adobe arabic",
      "adobe arabic bold",
      "adobearabic-bold",
      "other face",
    ]);
  });

  it("keeps reading a collection past a malformed font", () => {
    const firstAt = 20;
    const collection = new Uint8Array(firstAt + font.length);
    const view = new DataView(collection.buffer);
    collection.set([..."ttcf"].map((c) => c.charCodeAt(0)));
    view.setUint32(4, 0x00010000);
    view.setUint32(8, 2);
    view.setUint32(12, collection.length + 100);
    view.setUint32(16, firstAt);
    collection.set(relocate(font, firstAt), firstAt);

    expect(fontFamilyNames(collection).sort()).toEqual([
      "adobe arabic",
      "adobe arabic bold",
      "adobearabic-bold",
    ]);
  });

  it("stops reading names once a font's parsing budget is spent", () => {
    const long = (n: number) => `Family ${n} ${"x".repeat(30_000)}`;
    const crafted = sfntWithNames([
      ...[1, 2, 3, 4, 5].map((n) => ({ platform: 3, encoding: 1, nameId: 1, value: long(n) })),
      { platform: 3, encoding: 1, nameId: 1, value: "Late Name" },
    ]);

    const names = fontFamilyNames(crafted);
    expect(names).toHaveLength(4);
    expect(names).not.toContain("late name");
  });

  it("stops reading a collection once its records have used the parsing budget", () => {
    // Four fonts list 600 other tables before one shared name table of 65,000
    // records no style can name, so they decode nothing; together they spend
    // the budget, and the fifth font's own name is never read.
    const records = 65_000;
    const otherTables = 600;
    const shared = 6 + records * 12;
    const header = 12 + 5 * 4;
    const faceSize = 12 + (otherTables + 1) * 16;
    const faceAt = (i: number) => header + i * faceSize;
    const sharedAt = faceAt(4);
    const late = sfntWithNames([{ platform: 3, encoding: 1, nameId: 1, value: "Late Name" }]);
    const lateAt = sharedAt + shared;
    const collection = new Uint8Array(lateAt + late.length);
    const view = new DataView(collection.buffer);
    collection.set([..."ttcf"].map((c) => c.charCodeAt(0)));
    view.setUint32(4, 0x00010000);
    view.setUint32(8, 5);
    for (let i = 0; i < 4; i += 1) {
      const at = faceAt(i);
      view.setUint32(12 + i * 4, at);
      view.setUint32(at, 0x00010000);
      view.setUint16(at + 4, otherTables + 1);
      const nameRecord = at + 12 + otherTables * 16;
      collection.set(
        [..."name"].map((c) => c.charCodeAt(0)),
        nameRecord,
      );
      view.setUint32(nameRecord + 8, sharedAt);
    }
    view.setUint32(12 + 4 * 4, lateAt);
    view.setUint16(sharedAt + 2, records);
    view.setUint16(sharedAt + 4, shared);
    for (let j = 0; j < records; j += 1) view.setUint16(sharedAt + 6 + j * 12 + 6, 2);
    collection.set(relocate(late, lateAt), lateAt);

    expect(fontFamilyNames(collection)).toEqual([]);
  });

  it("returns no names for data that is not an sfnt font", () => {
    expect(fontFamilyNames(new Uint8Array([1, 2, 3]))).toEqual([]);
    expect(fontFamilyNames(new TextEncoder().encode("wOF2 not parsed here"))).toEqual([]);
  });
});

/** Table offsets in a collection are from the start of the file. */
function relocate(font: Uint8Array, base: number): Uint8Array {
  const copy = font.slice();
  const view = new DataView(copy.buffer);
  view.setUint32(20, view.getUint32(20) + base);
  return copy;
}
