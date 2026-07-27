// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  deleteReaderFont,
  fetchReaderFonts,
  readerFontFileUrl,
  uploadReaderFont,
} from "./readerFontsApi";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("readerFontsApi", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches and unwraps the reader font list", async () => {
    const fonts = [
      { id: 3, name: "Literata", filename: "literata.woff2", created_at: "2026-07-19T00:00:00Z" },
    ];
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(String(input)).toBe("/api/v1/ebooks/reader-fonts");
      expect(init?.method ?? "GET").toBe("GET");
      return jsonResponse({ fonts });
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchReaderFonts()).resolves.toEqual(fonts);
  });

  it("tolerates a missing fonts array in the envelope", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => jsonResponse({})),
    );

    await expect(fetchReaderFonts()).resolves.toEqual([]);
  });

  it("uploads a font as multipart form data under the 'font' field", async () => {
    const created = {
      id: 5,
      name: "custom.ttf",
      filename: "custom.ttf",
      created_at: "2026-07-20T00:00:00Z",
    };
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(String(input)).toBe("/api/v1/ebooks/reader-fonts");
      expect(init?.method).toBe("POST");
      expect(init?.body).toBeInstanceOf(FormData);
      const body = init?.body as FormData;
      const uploaded = body.get("font");
      expect(uploaded).toBeInstanceOf(File);
      expect((uploaded as File).name).toBe("custom.ttf");
      return jsonResponse(created, 201);
    });
    vi.stubGlobal("fetch", fetchMock);

    const file = new File(["binary"], "custom.ttf", { type: "font/ttf" });
    await expect(uploadReaderFont(file)).resolves.toEqual(created);
  });

  it("surfaces the server error message and code on a failed upload", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        jsonResponse({ error: "too_large", message: "Font file exceeds the maximum size" }, 413),
      ),
    );

    const file = new File(["binary"], "huge.ttf", { type: "font/ttf" });
    await expect(uploadReaderFont(file)).rejects.toMatchObject({
      status: 413,
      code: "too_large",
      message: "Font file exceeds the maximum size",
    });
  });

  it("deletes a font by id", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(String(input)).toBe("/api/v1/ebooks/reader-fonts/3");
      expect(init?.method).toBe("DELETE");
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(deleteReaderFont(3)).resolves.toBeUndefined();
  });

  it("builds the reader font file url", () => {
    expect(readerFontFileUrl(3)).toBe("/api/v1/ebooks/reader-fonts/3/file");
  });
});
