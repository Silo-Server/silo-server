import { describe, expect, it } from "vitest";

import { buildAdminPlaybackHistoryQuery } from "@/hooks/queries/admin/history";
import {
  buildActivityBucketGrid,
  buildHistoryDrilldownHref,
  buildInsightsWindowQueryParams,
  formatActivityBucketLabel,
  formatRatePercentNumber,
  parseInsightsUrlState,
  widgetRangeToInsightsRange,
} from "./insightsHelpers";

describe("parseInsightsUrlState", () => {
  it("defaults to the live activity tab, 30d range, and browser timezone", () => {
    const state = parseInsightsUrlState(new URLSearchParams(), "America/New_York");
    expect(state.tab).toBe("activity");
    expect(state.range).toBe("30d");
    expect(state.tz).toBe("America/New_York");
    expect(state.libraryId).toBeUndefined();
    expect(state.userId).toBeUndefined();
  });

  it("maps dashboard widget ranges", () => {
    expect(widgetRangeToInsightsRange("hour")).toBe("7d");
    expect(widgetRangeToInsightsRange("day")).toBe("7d");
    expect(widgetRangeToInsightsRange("week")).toBe("7d");
    expect(widgetRangeToInsightsRange("month")).toBe("30d");
  });

  it("parses explicit tab, custom range dates, timezone, library, and account", () => {
    const params = new URLSearchParams(
      "tab=devices&range=custom&from=2026-10-01&to=2026-10-15&tz=UTC&library_id=4&user_id=12",
    );
    const state = parseInsightsUrlState(params, "America/New_York");
    expect(state).toEqual({
      tab: "devices",
      range: "custom",
      fromDate: "2026-10-01",
      toDate: "2026-10-15",
      tz: "UTC",
      libraryId: 4,
      userId: 12,
    });
    expect(buildInsightsWindowQueryParams(state)).toEqual({
      range: "custom",
      tz: "UTC",
      from_date: "2026-10-01",
      to_date: "2026-10-15",
      library_id: "4",
      user_id: "12",
    });
  });

  it("ignores invalid tab, range, and non-positive numeric filter params", () => {
    const params = new URLSearchParams("tab=bogus&range=999d&library_id=-3&user_id=abc");
    const state = parseInsightsUrlState(params, "UTC");
    expect(state.tab).toBe("activity");
    expect(state.range).toBe("30d");
    expect(state.libraryId).toBeUndefined();
    expect(state.userId).toBeUndefined();
  });
});

describe("buildHistoryDrilldownHref & buildAdminPlaybackHistoryQuery", () => {
  it("round-trips multi-facet Insights drill-down filters into v2 history query", () => {
    const href = buildHistoryDrilldownHref({
      userId: "7",
      profileId: "p-alice",
      libraryId: 2,
      playMethod: "transcode",
      clientName: "Chrome",
      devicePlatform: "web",
      sourceVideoCodec: "hevc",
      adaptationReason: "subtitle_burn_in",
      subtitleBurnIn: true,
      streamLocation: "remote",
      startedAfter: "2026-11-01T00:00:00Z",
      startedBefore: "2026-11-22T00:00:00Z",
    });
    const parsed = new URL(href, "https://silo.example").searchParams;
    const query = buildAdminPlaybackHistoryQuery({
      userId: Number(parsed.get("user_id")),
      profileId: parsed.get("profile_id") ?? undefined,
      libraryId: Number(parsed.get("library_id")),
      playMethod: parsed.get("play_method") ?? undefined,
      clientName: parsed.get("client_name") ?? undefined,
      devicePlatform: parsed.get("device_platform") ?? undefined,
      sourceVideoCodec: parsed.get("source_video_codec") ?? undefined,
      adaptationReason: parsed.get("adaptation_reason") ?? undefined,
      subtitleBurnIn: parsed.get("subtitle_burn_in") === "true" ? "true" : "all",
      streamLocation: (parsed.get("stream_location") as "remote" | "local" | null) ?? "all",
      startedAfter: parsed.get("started_after") ?? undefined,
      startedBefore: parsed.get("started_before") ?? undefined,
      search: "  dune ",
      limit: 50,
    });
    expect(query).toEqual({
      limit: 50,
      user_id: "7",
      profile_id: "p-alice",
      library_id: "2",
      play_method: "transcode",
      client_name: "Chrome",
      device_platform: "web",
      source_video_codec: "hevc",
      adaptation_reason: "subtitle_burn_in",
      subtitle_burn_in: "true",
      stream_location: "remote",
      started_after: "2026-11-01T00:00:00Z",
      started_before: "2026-11-22T00:00:00Z",
      search: "dune",
    });
  });

  it("formats fractional rates below 10% with one decimal and clamps bounds", () => {
    expect(formatRatePercentNumber(0)).toBe("0");
    expect(formatRatePercentNumber(0.042)).toBe("4.2");
    expect(formatRatePercentNumber(0.876)).toBe("88");
    expect(formatRatePercentNumber(1.2)).toBe("100");
  });
});

describe("buildInsightsWindowQueryParams scope omission", () => {
  it("omits library_id and user_id when callers scope out those filters", () => {
    const state = parseInsightsUrlState(
      new URLSearchParams("range=30d&tz=UTC&library_id=4&user_id=12"),
      "UTC",
    );
    expect(buildInsightsWindowQueryParams(state, { omitUser: true })).toEqual({
      range: "30d",
      tz: "UTC",
      library_id: "4",
    });
    expect(buildInsightsWindowQueryParams(state, { omitLibrary: true, omitUser: true })).toEqual({
      range: "30d",
      tz: "UTC",
    });
  });
});

describe("buildActivityBucketGrid & formatActivityBucketLabel", () => {
  it("zero-fills daily buckets in a non-UTC timezone across DST without skipping or duplicating days", () => {
    // 2026-11-01 00:00 EDT (04:00Z) through 2026-11-04 00:00 EST (05:00Z) spans the Nov 1 DST fallback.
    expect(
      buildActivityBucketGrid(
        "2026-11-01T04:00:00Z",
        "2026-11-04T05:00:00Z",
        "America/New_York",
        "day",
      ),
    ).toEqual(["2026-11-01", "2026-11-02", "2026-11-03"]);
  });

  it("aligns weekly buckets to Monday and enumerates months cleanly", () => {
    // 2026-11-04 is a Wednesday -> Postgres date_trunc('week', ...) starts on Monday 2026-11-02.
    expect(
      buildActivityBucketGrid("2026-11-04T00:00:00Z", "2026-11-18T00:00:00Z", "UTC", "week"),
    ).toEqual(["2026-11-02", "2026-11-09", "2026-11-16"]);

    expect(
      buildActivityBucketGrid("2026-10-15T00:00:00Z", "2027-01-10T00:00:00Z", "UTC", "month"),
    ).toEqual(["2026-10-01", "2026-11-01", "2026-12-01", "2027-01-01"]);
  });

  it("formats UTC calendar bucket timestamps without shifting a day behind in western timezones", () => {
    const label = formatActivityBucketLabel(Date.parse("2026-11-01T00:00:00Z"), "day");
    expect(label).toContain("1");
    expect(label).not.toContain("31");
    const monthLabel = formatActivityBucketLabel(Date.parse("2026-11-01T00:00:00Z"), "month");
    expect(monthLabel).toContain("2026");
  });
});
