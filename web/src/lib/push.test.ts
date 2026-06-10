import { describe, expect, it } from "vitest";
import { urlBase64ToUint8Array } from "./push";

describe("urlBase64ToUint8Array", () => {
  it("decodes a url-safe base64 key to bytes", () => {
    // "AQID" → [1,2,3]
    expect(Array.from(urlBase64ToUint8Array("AQID"))).toEqual([1, 2, 3]);
  });
  it("handles url-safe chars and missing padding", () => {
    const out = urlBase64ToUint8Array("a-_9"); // '-'→'+', '_'→'/'
    expect(out.length).toBe(3);
  });
});
