import { describe, expect, it } from "vitest";
import {
  buildDefaultPrefs,
  overlayDataFromWatchlistTitle,
  requestDownloadBarPercent,
  SAMPLE_REQUEST_DATA,
} from "@/lib/overlays";

describe("overlayDataFromWatchlistTitle", () => {
  it("carries the status badge, icon, attention and download progress", () => {
    expect(
      overlayDataFromWatchlistTitle(
        { vote_average: 7.9, content_rating: "R", year: 1995 },
        { badge: "Downloading 43%", badgeIcon: "download", attention: false, downloadPercent: 43 },
      ),
    ).toEqual({
      rating_tmdb: 7.9,
      content_rating: "R",
      year: 1995,
      request_status: "Downloading 43%",
      request_status_icon: "download",
      request_status_attention: false,
      request_download_percent: 43,
    });
  });
});

describe("requestDownloadBarPercent", () => {
  it("draws the bar only while the request status badge is on", () => {
    const prefs = buildDefaultPrefs();
    expect(prefs.items.request_status.enabled).toBe(true);
    expect(requestDownloadBarPercent(SAMPLE_REQUEST_DATA, prefs)).toBe(43);

    prefs.items.request_status = { ...prefs.items.request_status, enabled: false };
    expect(requestDownloadBarPercent(SAMPLE_REQUEST_DATA, prefs)).toBeNull();
  });

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
