import { afterEach, describe, expect, it } from "vitest";
import { storage } from "@/utils/storage";
import { isBrowserDecodingEnabled, setBrowserDecodingEnabled } from "./settings";

describe("browser decoding setting", () => {
  afterEach(() => localStorage.clear());

  it("is on when this browser has never chosen", () => {
    expect(isBrowserDecodingEnabled()).toBe(true);
  });

  it("stays off once the viewer turns it off", () => {
    setBrowserDecodingEnabled(false);
    expect(storage.get(storage.KEYS.BROWSER_DECODING)).toBe("false");
    expect(isBrowserDecodingEnabled()).toBe(false);
  });
});
