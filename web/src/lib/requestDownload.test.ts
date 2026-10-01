import { describe, expect, it } from "vitest";
import type { RequestDownload } from "@/api/types";
import { formatRequestDownload, requestDownloadPercent } from "./requestDownload";

describe("request download progress", () => {
  const now = new Date("2026-01-02T03:04:05Z");
  const download = (overrides: Partial<RequestDownload> = {}): RequestDownload => ({
    phase: "downloading",
    percent: 43,
    bytes_total: 4294967296,
    bytes_left: 2448131358,
    estimated_completion_at: "2026-01-02T03:16:05Z",
    downloads: 1,
    updated_at: "2026-01-02T03:03:05Z",
    ...overrides,
  });

  it("names the phase, the percentage and the time left while downloading", () => {
    expect(formatRequestDownload(download(), { now })).toBe(
      "Downloading · 43% · about 12 min left",
    );
    expect(
      formatRequestDownload(download({ estimated_completion_at: "2026-01-02T05:34:05Z" }), {
        now,
      }),
    ).toBe("Downloading · 43% · about 2 hr 30 min left");
  });

  it("leaves out what the server does not know yet", () => {
    expect(
      formatRequestDownload(
        download({
          percent: undefined,
          bytes_total: undefined,
          bytes_left: undefined,
          estimated_completion_at: undefined,
        }),
        { now },
      ),
    ).toBe("Downloading");
  });

  it("drops an estimate that has passed or comes from figures over ten minutes old", () => {
    expect(
      formatRequestDownload(download({ estimated_completion_at: "2026-01-02T03:04:00Z" }), {
        now,
      }),
    ).toBe("Downloading · 43%");
    expect(formatRequestDownload(download({ updated_at: "2026-01-02T02:54:04Z" }), { now })).toBe(
      "Downloading · 43%",
    );
    // Exactly ten minutes old is still recent enough.
    expect(formatRequestDownload(download({ updated_at: "2026-01-02T02:54:05Z" }), { now })).toBe(
      "Downloading · 43% · about 12 min left",
    );
  });

  it.each([
    ["queued", "Waiting to download"],
    ["paused", "Download paused"],
    ["stalled", "Download stalled"],
    ["importing", "Importing"],
    ["import_blocked", "Waiting for import"],
  ])("labels %s as %s", (phase, label) => {
    expect(formatRequestDownload(download({ phase }), { now })).toBe(label);
  });

  it("tells an admin that an import is blocked", () => {
    expect(formatRequestDownload(download({ phase: "import_blocked" }), { now, admin: true })).toBe(
      "Import blocked",
    );
    expect(formatRequestDownload(download(), { now, admin: true })).toBe(
      "Downloading · 43% · about 12 min left",
    );
  });

  it("reads a phase it does not know as downloading, without figures", () => {
    const unknown = download({ phase: "seeding" });
    expect(formatRequestDownload(unknown, { now })).toBe("Downloading");
    expect(requestDownloadPercent(unknown)).toBeUndefined();
  });

  it("gives a bar percentage once the size is known, clamped to 0–100", () => {
    expect(requestDownloadPercent(download())).toBe(43);
    expect(requestDownloadPercent(download({ phase: "paused" }))).toBe(43);
    expect(requestDownloadPercent(download({ percent: undefined }))).toBeUndefined();
    expect(requestDownloadPercent(download({ percent: 140 }))).toBe(100);
    expect(requestDownloadPercent(download({ percent: -3 }))).toBe(0);
  });
});
