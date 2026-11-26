import { describe, expect, it } from "vitest";
import { MAX_SCAN_CODE_ADDRESS_BYTES, scanCodeText } from "./scanCode";
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

describe("scanCodeText", () => {
  it("has valid and refused cases", () => {
    expect(valid.length).toBeGreaterThan(0);
    expect(cases.length).toBeGreaterThan(valid.length);
  });

  // A text the app's scanner reads differently would strand the person on
  // the code step with a scan code that does nothing.
  it.each(valid.map((c) => [c.name, c] as const))("writes %s", (_, c) => {
    expect(scanCodeText(c.app_code!, c.address!)).toBe(c.string);
  });

  // The scanner refuses an address that isn't in its one spelling or is over
  // 128 bytes; drawing one would show a code that can't be read.
  const refusedAddresses = cases
    .filter((c) => !c.valid && (c.error === "address" || c.error === "too_long"))
    .map((c) => [c.name, carriedAddress(c.string)] as const)
    .filter((entry): entry is readonly [string, string] => entry[1] !== null);
  it.each(refusedAddresses)("draws nothing for %s", (_, address) => {
    expect(scanCodeText(appCode, address)).toBeNull();
  });

  // Rule: a server address longer than 128 bytes has no scan code.
  it("draws nothing for an address one byte over the limit", () => {
    const base = "https://example.com/";
    const longest = base + "a".repeat(MAX_SCAN_CODE_ADDRESS_BYTES - base.length);
    expect(scanCodeText(appCode, longest)).not.toBeNull();
    expect(scanCodeText(appCode, `${longest}a`)).toBeNull();
  });
});
