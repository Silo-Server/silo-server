import { buildDefaultPrefs, requestDownloadBarPercent, SAMPLE_REQUEST_DATA } from "@/lib/overlays";
import { describe, expect, it } from "vitest";

describe("requestDownloadBarPercent", () => {
  it("draws no bar with overlays off, without a status, or without a known size", () => {
    const prefs = buildDefaultPrefs();
    expect(requestDownloadBarPercent(SAMPLE_REQUEST_DATA, null)).toBeNull();
    expect(
      requestDownloadBarPercent({ ...SAMPLE_REQUEST_DATA, request_status: undefined }, prefs),
    ).toBeNull();
    expect(
      requestDownloadBarPercent({ ...SAMPLE_REQUEST_DATA, request_download_percent: null }, prefs),
    ).toBeNull();
  });

  it("clamps the percent to the bar", () => {
    expect(
      requestDownloadBarPercent(
        { ...SAMPLE_REQUEST_DATA, request_download_percent: 140 },
        buildDefaultPrefs(),
      ),
    ).toBe(100);
  });
});
