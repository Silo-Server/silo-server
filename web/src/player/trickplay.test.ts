// @vitest-environment node

import { describe, expect, it } from "vitest";
import { trickplayTile, type PlayerTrickplay } from "./trickplay";
import { trickplayFromV2 } from "@/api/v2/trickplay";

const manifest: PlayerTrickplay = {
  intervalMs: 10_000,
  width: 300,
  height: 126,
  columns: 10,
  rows: 10,
  count: 250,
  sheets: ["s0", "s1", "s2"],
  expiresAt: 0,
};

describe("trickplayTile", () => {
  it("places a time on its sheet, column and row", () => {
    expect(trickplayTile(manifest, 0)).toEqual({ sheet: 0, url: "s0", x: 0, y: 0 });
    expect(trickplayTile(manifest, 9.99)).toEqual({ sheet: 0, url: "s0", x: 0, y: 0 });
    expect(trickplayTile(manifest, 10)).toEqual({ sheet: 0, url: "s0", x: 300, y: 0 });
    expect(trickplayTile(manifest, 305)).toEqual({ sheet: 0, url: "s0", x: 0, y: 378 });
    expect(trickplayTile(manifest, 1234)).toEqual({ sheet: 1, url: "s1", x: 900, y: 252 });
  });

  it("keeps times past the last thumbnail on it and rejects nonsense", () => {
    expect(trickplayTile(manifest, 99_999)).toEqual({ sheet: 2, url: "s2", x: 2700, y: 504 });
    expect(trickplayTile(manifest, -5)).toEqual({ sheet: 0, url: "s0", x: 0, y: 0 });
    expect(trickplayTile(manifest, Number.NaN)).toBeNull();
    expect(trickplayTile({ ...manifest, sheets: ["s0"] }, 1234)).toBeNull();
    expect(trickplayTile({ ...manifest, count: 0 }, 5)).toBeNull();
  });
});

describe("trickplayFromV2", () => {
  it("orders sheets by index and reads the expiry", () => {
    const player = trickplayFromV2({
      file_id: "42",
      interval_ms: 10_000,
      thumbnail_width: 300,
      thumbnail_height: 126,
      tile_columns: 10,
      tile_rows: 10,
      thumbnail_count: 150,
      sheets: [
        { index: 1, url: "b" },
        { index: 0, url: "a" },
      ],
      expires_at: "2026-01-02T03:04:05.000Z",
    });
    expect(player.sheets).toEqual(["a", "b"]);
    expect(player.expiresAt).toBe(Date.parse("2026-01-02T03:04:05.000Z"));
    expect(player.count).toBe(150);
  });
});
