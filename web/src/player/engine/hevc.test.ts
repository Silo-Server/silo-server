import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  hevcLengthSize,
  prepareHevcAccessUnit,
  retagHevcDecoderConfig,
  retagHevcSps,
} from "./hevc";

interface SpsFixture {
  name: string;
  sps: string;
  retagged: string | null;
}

const here = (name: string) => fileURLToPath(new URL(name, import.meta.url));
const { fixtures } = JSON.parse(readFileSync(here("./testdata/hevc-sps.json"), "utf8")) as {
  fixtures: SpsFixture[];
};
const bytes = (hex: string) => Uint8Array.from(hex.match(/../g)!, (pair) => parseInt(pair, 16));
const hex = (data: Uint8Array) => Buffer.from(data).toString("hex");
const fixture = (name: string) => fixtures.find((entry) => entry.name === name)!;

/** One 4-byte length-prefixed NAL unit. */
function prefixed(unit: number[] | Uint8Array): number[] {
  const body = Array.from(unit);
  return [0, 0, 0, body.length, ...body];
}

/** A NAL unit with the given type and layer id. */
function nal(type: number, layer = 0, payload = [0xaa, 0xbb]): number[] {
  return [(type << 1) | (layer >> 5), ((layer & 0x1f) << 3) | 1, ...payload];
}

describe("retagHevcSps", () => {
  // Each fixture's expected unit is what ffmpeg's hevc_metadata filter writes,
  // so the SPS walk is checked against an independent parser.
  it.each(fixtures)("rewrites the colour description like ffmpeg ($name)", (entry) => {
    const result = retagHevcSps(bytes(entry.sps));
    expect(result === null ? null : hex(result)).toBe(entry.retagged);
  });

  it("leaves an SPS that already carries the import tag alone", () => {
    expect(retagHevcSps(bytes(fixture("x265").retagged!))).toBeNull();
  });

  it("gives up on a truncated SPS", () => {
    expect(retagHevcSps(bytes(fixture("x265").sps).subarray(0, 20))).toBeNull();
  });
});

describe("prepareHevcAccessUnit", () => {
  const sps = bytes(fixture("x265").sps);
  const retagged = bytes(fixture("x265").retagged!);

  it("strips Dolby Vision units and retags the SPS in one pass", () => {
    const vps = nal(32);
    const slice = nal(19);
    const input = new Uint8Array([
      ...prefixed(vps),
      ...prefixed(sps),
      ...prefixed(nal(62)),
      ...prefixed(slice),
      ...prefixed(nal(63)),
      ...prefixed(nal(1, 1)),
    ]);
    const output = prepareHevcAccessUnit(input, 4, { stripDolbyVision: true, retagColor: true });
    expect(Array.from(output)).toEqual([
      ...prefixed(vps),
      ...prefixed(retagged),
      ...prefixed(slice),
    ]);
  });

  it("keeps Dolby Vision units and the SPS when not asked to change them", () => {
    const input = new Uint8Array([...prefixed(sps), ...prefixed(nal(62))]);
    expect(prepareHevcAccessUnit(input, 4, { stripDolbyVision: false, retagColor: false })).toBe(
      input,
    );
  });

  it("returns the same buffer when there is nothing to change", () => {
    const input = new Uint8Array([...prefixed(nal(19)), ...prefixed(nal(1))]);
    expect(prepareHevcAccessUnit(input, 4, { stripDolbyVision: true, retagColor: true })).toBe(
      input,
    );
  });

  it("leaves data that does not parse as length-prefixed units untouched", () => {
    const input = new Uint8Array([0, 0, 1, 0x40, 0x01]);
    expect(prepareHevcAccessUnit(input, 4, { stripDolbyVision: true, retagColor: true })).toBe(
      input,
    );
  });
});

describe("retagHevcDecoderConfig", () => {
  /** An hvcC record holding one array per unit, with `trailer` after the arrays. */
  function record(units: Array<number[] | Uint8Array>, trailer: number[] = []): Uint8Array {
    const header = new Array<number>(23).fill(0);
    header[0] = 1;
    header[21] = 0xf3;
    header[22] = units.length;
    const arrays = units.flatMap((unit) => {
      const body = Array.from(unit);
      return [0x80 | ((body[0]! >> 1) & 0x3f), 0, 1, body.length >> 8, body.length & 0xff, ...body];
    });
    return new Uint8Array([...header, ...arrays, ...trailer]);
  }

  it("retags the SPS and keeps the rest of the record", () => {
    const vps = nal(32);
    const pps = nal(34);
    const entry = fixture("x265sl");
    const output = retagHevcDecoderConfig(record([vps, bytes(entry.sps), pps], [0xde, 0xad]));
    expect(hex(output)).toBe(hex(record([vps, bytes(entry.retagged!), pps], [0xde, 0xad])));
  });

  it("returns the record itself when no SPS needs rewriting", () => {
    const input = record([nal(32), bytes(fixture("nocolour").sps)]);
    expect(retagHevcDecoderConfig(input)).toBe(input);
  });

  it("returns a truncated record unchanged", () => {
    const input = record([bytes(fixture("x265").sps)]).subarray(0, 40);
    expect(retagHevcDecoderConfig(input)).toBe(input);
  });
});

describe("hevcLengthSize", () => {
  it("reads lengthSizeMinusOne from an hvcC record", () => {
    const record = new Uint8Array(23);
    record[21] = 0xf3;
    expect(hevcLengthSize(record)).toBe(4);
    expect(hevcLengthSize(new Uint8Array(4))).toBeNull();
  });
});
