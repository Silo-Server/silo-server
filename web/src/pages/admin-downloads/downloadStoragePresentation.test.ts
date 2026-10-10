import { describe, expect, it } from "vitest";
import { makeStorage, makeStorageFile, makeStorageLocation } from "@/test/downloadStorage";
import {
  formatExpiresIn,
  formatStorageBytes,
  meterSegments,
  recordsDrift,
  storageEventItem,
  storageTotals,
  storageWarnings,
} from "./downloadStoragePresentation";

describe("formatStorageBytes", () => {
  it("uses decimal units like the budget settings", () => {
    expect(formatStorageBytes(0)).toBe("0 B");
    expect(formatStorageBytes(500e9)).toBe("500 GB");
    expect(formatStorageBytes(38e9)).toBe("38 GB");
    expect(formatStorageBytes(2.3e9)).toBe("2.3 GB");
    expect(formatStorageBytes(1.1e12)).toBe("1.1 TB");
    expect(formatStorageBytes(999.7e9)).toBe("1.0 TB");
    expect(formatStorageBytes(undefined)).toBe("0 B");
  });
});

describe("meterSegments", () => {
  it("splits the filesystem into prepared files, other data and free space", () => {
    const segments = meterSegments(makeStorageLocation())!;
    const bytes = Object.fromEntries(segments.map((s) => [s.kind, s.bytes]));
    expect(bytes).toEqual({
      in_use: 162e9,
      cached: 52e9,
      untracked: 0,
      other: 1006e9,
      free: 780e9,
    });
    expect(segments.reduce((sum, s) => sum + s.fraction, 0)).toBeCloseTo(1);
  });

  it("never draws more prepared bytes than the directory holds", () => {
    const location = makeStorageLocation({ in_use_bytes: 300e9, cached_bytes: 100e9 });
    const segments = meterSegments(location)!;
    const prepared = segments
      .filter((s) => s.kind === "in_use" || s.kind === "cached")
      .reduce((sum, s) => sum + s.bytes, 0);
    expect(prepared).toBe(214e9);
  });

  it("is null without a measurement", () => {
    expect(meterSegments(makeStorageLocation({ usage: undefined }))).toBeNull();
  });
});

describe("storageWarnings", () => {
  it("warns about a scratch disk near the ceiling first", () => {
    const node = makeStorageLocation({
      key: "node:9",
      kind: "node",
      name: "node-gpu-1",
      usage: {
        ...makeStorageLocation().usage!,
        fs_used_bytes: 842e9,
        fs_total_bytes: 1000e9,
        shares_scratch: true,
      },
      untracked_bytes: 38e9,
      untracked_files: 6,
    });
    const warnings = storageWarnings(makeStorage({ locations: [makeStorageLocation(), node] }));
    expect(warnings[0]).toMatchObject({
      tone: "warning",
      location: "node:9",
      action: "edit_location",
    });
    expect(warnings[0]?.title).toBe("node-gpu-1 is at 84% disk.");
    expect(warnings[0]?.body).toContain("stops taking playback");
    expect(warnings.some((w) => w.action === "review_untracked")).toBe(true);
  });

  it("flags temporary storage, offline nodes and stale devices", () => {
    const tmp = makeStorageLocation({
      usage: { ...makeStorageLocation().usage!, ephemeral: true, fs_type: "overlay" },
    });
    const offline = makeStorageLocation({
      key: "node:3",
      kind: "node",
      name: "node-cpu-3",
      online: false,
      stale_waiting_bytes: 11e9,
    });
    const ids = storageWarnings(makeStorage({ locations: [tmp, offline] })).map((w) => w.id);
    expect(ids).toEqual(expect.arrayContaining(["ephemeral:server", "offline:node:3", "stale"]));
  });

  it("is empty for a healthy server", () => {
    expect(storageWarnings(makeStorage())).toEqual([]);
  });
});

describe("totals and drift", () => {
  it("adds up every location", () => {
    const node = makeStorageLocation({ key: "node:9", kind: "node", untracked_bytes: 38e9 });
    const totals = storageTotals(makeStorage({ locations: [makeStorageLocation(), node] }));
    expect(totals).toMatchObject({ inUse: 324e9, cached: 104e9, untracked: 38e9, files: 624 });
  });

  it("reports drift only past 1 GB or 2%", () => {
    expect(recordsDrift(makeStorageLocation())).toBe(0);
    expect(recordsDrift(makeStorageLocation({ cached_bytes: 40e9 }))).toBe(12e9);
  });
});

describe("files and history", () => {
  it("formats when a cached file expires", () => {
    const now = Date.parse("2026-10-08T10:00:00Z");
    expect(formatExpiresIn("2026-10-09T08:00:00Z", now)).toBe("in 22 h");
    expect(formatExpiresIn("2026-10-12T10:00:00Z", now)).toBe("in 4 d");
    expect(formatExpiresIn("2026-10-08T09:00:00Z", now)).toBe("now");
  });

  it("names a revoke batch by device and account", () => {
    expect(
      storageEventItem({
        id: "b",
        reason: "revoked",
        location: "device",
        location_name: "Pixel 8",
        occurred_at: "",
        count: 12,
        bytes: 0,
        titles: [],
        account: { id: "7", username: "maya" },
      }),
    ).toBe("12 downloads · Pixel 8 · maya");
    expect(makeStorageFile().title).toBe("Dune: Part Two");
  });
});
