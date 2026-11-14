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
