import { describe, expect, it } from "vitest";
import {
  MAX_SCAN_CODE_ADDRESS_BYTES,
  MIN_MODULE_PIXELS,
  scanCode,
  scanCodePixels,
} from "./scanCode";
import vectors from "./scanCode.vectors.json";

type ScanCodeCase = {
  name: string;
  valid: boolean;
  error?: string;
  string: string;
  address?: string;
  app_code?: string;
};

const cases = vectors.cases as ScanCodeCase[];
const valid = cases.filter((c) => c.valid);
const appCode = valid[0]?.app_code ?? "";
const longest = valid.find(
  (c) => c.string.length === Math.max(...valid.map((v) => v.string.length)),
)!;

/** The address a refused text carries, when its letters decode to UTF-8 text. */
function carriedAddress(text: string): string | null {
  const letters = text.split(" ")[2];
  if (!letters || !/^(?:[A-P]{2})+$/.test(letters)) return null;
  const bytes = Uint8Array.from(letters.match(/../g)!, (pair) => {
    return (pair.charCodeAt(0) - 65) * 16 + (pair.charCodeAt(1) - 65);
  });
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return null;
  }
}

describe("scanCode", () => {
  it("has valid and refused cases", () => {
    expect(valid.length).toBeGreaterThan(0);
    expect(cases.length).toBeGreaterThan(valid.length);
  });

  // A text the app's scanner reads differently would strand the person on
  // the code step with a scan code that does nothing.
  it.each(valid.map((c) => [c.name, c] as const))("writes %s", (_, c) => {
    expect(scanCode(c.app_code!, c.address!)).toEqual({ text: c.string });
  });

  // The scanner refuses an address that isn't in its one spelling or is over
  // 128 bytes; drawing one would show a code that can't be read.
  const refusedAddresses = cases
    .filter((c) => !c.valid && (c.error === "address" || c.error === "too_long"))
    .map((c) => [c.name, carriedAddress(c.string)] as const)
    .filter((entry): entry is readonly [string, string] => entry[1] !== null);
  it.each(refusedAddresses)("draws nothing for %s", (_, address) => {
    expect(scanCode(appCode, address).text).toBeNull();
  });

  // Addresses a URL round-trip accepts but the app's scanner refuses.
  it.each([
    ["an IPv4-mapped IPv6 host", "http://[::ffff:7f00:1]"],
    ["an empty path segment", "https://media.example.com/a//b"],
    ["a | in the path", "https://media.example.com/a|b"],
    ["brackets in the path", "https://media.example.com/a[b]"],
    ["a * in the host", "https://a*b.example.com"],
    ["a $ in the host", "https://a$b.example.com"],
    ["a ! in the host", "https://a!b.example.com"],
    ["a host label of 64 characters", `https://${"a".repeat(64)}.example.com`],
  ])("draws nothing for %s", (_, address) => {
    expect(scanCode(appCode, address)).toEqual({ text: null, reason: "not_drawable" });
  });

  // Rule: a server address longer than 128 bytes has no scan code, and the
  // page says it is too long (not merely undrawable).
  it("draws nothing for an address one byte over the limit", () => {
    const base = "https://example.com/";
    const limit = base + "a".repeat(MAX_SCAN_CODE_ADDRESS_BYTES - base.length);
    expect(scanCode(appCode, limit).text).not.toBeNull();
    expect(scanCode(appCode, `${limit}a`)).toEqual({ text: null, reason: "too_long" });
  });
});

describe("scanCodePixels", () => {
  // A module under 3.5 px is hard for a phone camera to resolve off a screen.
  // Byte mode at level L: a typical address is version 6 (41 modules), the
  // longest scan code version 12 (65 modules).
  it.each([
    ["a typical address", valid[0]!.string, 41, 176],
    ["the longest scan code", longest.string, 65, 228],
  ])("sizes %s", (_, text, modules, pixels) => {
    expect(scanCodePixels(text)).toBe(pixels);
    expect(pixels / modules).toBeGreaterThanOrEqual(MIN_MODULE_PIXELS);
  });
});
