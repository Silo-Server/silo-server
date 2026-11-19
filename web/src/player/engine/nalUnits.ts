/**
 * Splits one length-prefixed access unit (the `avcC`/`hvcC` layout) into the
 * `[start, end)` byte ranges of its NAL units, without the length fields.
 * Returns null when it does not parse: a length field or a unit runs past the
 * data, or a unit is shorter than `minLength` (its NAL header).
 */
export function nalUnitRanges(
  data: Uint8Array,
  lengthSize: number,
  minLength: number,
): Array<[start: number, end: number]> | null {
  const ranges: Array<[number, number]> = [];
  let offset = 0;
  while (offset < data.byteLength) {
    if (offset + lengthSize > data.byteLength) return null;
    let length = 0;
    for (let i = 0; i < lengthSize; i++) length = length * 256 + data[offset + i]!;
    const start = offset + lengthSize;
    const end = start + length;
    if (length < minLength || end > data.byteLength) return null;
    ranges.push([start, end]);
    offset = end;
  }
  return ranges;
}

/** Removes emulation prevention bytes (the 0x03 in 00 00 03) from a NAL unit's payload. */
export function unescapeRbsp(data: Uint8Array): Uint8Array {
  const out = new Uint8Array(data.byteLength);
  let length = 0;
  let zeros = 0;
  for (const byte of data) {
    if (zeros >= 2 && byte === 0x03) {
      zeros = 0;
      continue;
    }
    zeros = byte === 0 ? zeros + 1 : 0;
    out[length++] = byte;
  }
  return out.subarray(0, length);
}

/** Inserts the emulation prevention bytes `unescapeRbsp` removes. */
export function escapeRbsp(rbsp: Uint8Array): Uint8Array {
  const out = new Uint8Array(rbsp.byteLength + Math.ceil(rbsp.byteLength / 2));
  let length = 0;
  let zeros = 0;
  for (const byte of rbsp) {
    if (zeros >= 2 && byte <= 0x03) {
      out[length++] = 0x03;
      zeros = 0;
    }
    zeros = byte === 0 ? zeros + 1 : 0;
    out[length++] = byte;
  }
  return out.subarray(0, length);
}

/**
 * Reads the fixed-width and Exp-Golomb fields of an RBSP, most significant
 * bit first. Reading past the end throws a RangeError.
 */
export class BitReader {
  /** Bits read so far. */
  position = 0;

  constructor(private readonly data: Uint8Array) {}

  bit(): number {
    const byte = this.position >> 3;
    if (byte >= this.data.byteLength) throw new RangeError("read past the end of the RBSP");
    const value = (this.data[byte]! >> (7 - (this.position & 7))) & 1;
    this.position += 1;
    return value;
  }

  /** An unsigned fixed-width field, `u(n)`, of at most 32 bits. */
  bits(count: number): number {
    let value = 0;
    for (let i = 0; i < count; i++) value = value * 2 + this.bit();
    return value;
  }

  skip(count: number): void {
    if ((this.position + count + 7) >> 3 > this.data.byteLength) {
      throw new RangeError("read past the end of the RBSP");
    }
    this.position += count;
  }

  /** An unsigned Exp-Golomb field, `ue(v)`. */
  ue(): number {
    let zeros = 0;
    while (this.bit() === 0) {
      zeros += 1;
      if (zeros > 31) throw new RangeError("Exp-Golomb code too long");
    }
    return 2 ** zeros - 1 + this.bits(zeros);
  }

  /** A signed Exp-Golomb field, `se(v)`. */
  se(): number {
    const code = this.ue();
    return code % 2 === 1 ? (code + 1) / 2 : -code / 2;
  }
}
