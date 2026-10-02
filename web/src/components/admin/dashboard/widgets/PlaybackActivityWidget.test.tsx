import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminPlaybackActivity } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminPlaybackActivity: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/dashboardInsights", () => ({
  useAdminPlaybackActivity: mocks.useAdminPlaybackActivity,
}));

import { WidgetChromeProvider } from "../widgetChrome";
import { PlaybackActivityWidget } from "./PlaybackActivityWidget";
import { buildPlaybackActivityColumns } from "./playbackActivitySeries";

const HOUR_MS = 3_600_000;
const DAY_MS = 86_400_000;
const NOW = Date.parse("2026-08-26T14:37:00Z");
const CURRENT_HOUR = Math.floor(NOW / HOUR_MS) * HOUR_MS;
const CURRENT_DAY = Math.floor(NOW / DAY_MS) * DAY_MS;

function isoHour(hoursAgo: number): string {
  return new Date(CURRENT_HOUR - hoursAgo * HOUR_MS).toISOString();
}

function isoDay(daysAgo: number): string {
  return new Date(CURRENT_DAY - daysAgo * DAY_MS).toISOString();
}

function activity(overrides: Partial<AdminPlaybackActivity> = {}): AdminPlaybackActivity {
  return {
    hours: 24,
    bucket_seconds: 3600,
    buckets: [],
    reliability: {
      sessions_started: 0,
      transcode_starts: 0,
      finalized_sessions: 0,
      completed_sessions: 0,
      completion_rate: 0,
      unique_profiles: 0,
    },
    profiles_active_24h: 0,
    ...overrides,
  };
}

describe("buildPlaybackActivityColumns", () => {
  // The endpoint filters on `started_at >= now() - interval` and then truncates,
  // so at 14:37 a 24-hour window legitimately contains 25 hourly buckets: the
  // partial one that starts at 14:00 yesterday through the current hour.
  it("zero-fills the whole window when the response is empty", () => {
    const columns = buildPlaybackActivityColumns([], { now: NOW });

    expect(columns).toHaveLength(25);
    expect(columns[0]?.t).toBe(CURRENT_HOUR - 24 * HOUR_MS);
    expect(columns[24]?.t).toBe(CURRENT_HOUR);
    for (const column of columns) {
      expect([...column.segments]).toEqual([0, 0, 0]);
    }
  });

  // The leading bucket is only half inside the window, but every session the
  // endpoint counted for it is: dropping the column would lose them.
  it("keeps the leading partial bucket the endpoint can return", () => {
    const columns = buildPlaybackActivityColumns(
      [{ hour: isoHour(24), direct: 3, remux: 0, transcode: 1 }],
      { now: NOW },
    );

    expect(columns).toHaveLength(25);
    expect(columns[0]?.t).toBe(CURRENT_HOUR - 24 * HOUR_MS);
    expect([...(columns[0]?.segments ?? [])]).toEqual([3, 0, 1]);
  });

  it("places sparse buckets at their hour and leaves the quiet hours at zero", () => {
    const columns = buildPlaybackActivityColumns(
      [
        { hour: isoHour(2), direct: 4, remux: 1, transcode: 2 },
        { hour: isoHour(23), direct: 1, remux: 0, transcode: 0 },
      ],
      { now: NOW },
    );

    expect(columns).toHaveLength(25);
    expect([...(columns[22]?.segments ?? [])]).toEqual([4, 1, 2]);
    expect([...(columns[1]?.segments ?? [])]).toEqual([1, 0, 0]);
    expect([...(columns[23]?.segments ?? [])]).toEqual([0, 0, 0]);
    expect([...(columns[24]?.segments ?? [])]).toEqual([0, 0, 0]);
  });

  it("ignores buckets outside the window and unparseable timestamps", () => {
    const columns = buildPlaybackActivityColumns(
      [
        { hour: isoHour(48), direct: 9, remux: 9, transcode: 9 },
        { hour: "not-a-timestamp", direct: 7, remux: 7, transcode: 7 },
      ],
      { now: NOW },
    );

    expect(columns).toHaveLength(25);
    expect(columns.every((column) => column.segments.every((value) => value === 0))).toBe(true);
  });

  it("treats a missing response as an empty window", () => {
    expect(buildPlaybackActivityColumns(undefined, { now: NOW })).toHaveLength(25);
  });

  // Past two days the endpoint groups by day, and zero-filling on an hourly
  // grid would scatter those columns across empty hours.
  it("zero-fills a week on the daily grid the server bucketed by", () => {
    const columns = buildPlaybackActivityColumns(
      [{ hour: isoDay(3), direct: 5, remux: 0, transcode: 1 }],
      { hours: 168, bucketSeconds: 86_400, now: NOW },
    );

    // Eight columns, not seven: the day the window opens in is partial and the
    // endpoint still reports it.
    expect(columns).toHaveLength(8);
    expect(columns[0]?.t).toBe(CURRENT_DAY - 7 * DAY_MS);
    expect(columns[7]?.t).toBe(CURRENT_DAY);
    expect([...(columns[4]?.segments ?? [])]).toEqual([5, 0, 1]);
    expect([...(columns[5]?.segments ?? [])]).toEqual([0, 0, 0]);
  });

  it("covers a month with 31 daily columns", () => {
    const columns = buildPlaybackActivityColumns([], {
      hours: 720,
      bucketSeconds: 86_400,
      now: NOW,
    });

    expect(columns).toHaveLength(31);
  });

  it("falls back to hourly buckets when the response omits the width", () => {
    const columns = buildPlaybackActivityColumns([], { hours: 24, bucketSeconds: 0, now: NOW });

    expect(columns).toHaveLength(25);
  });
});

describe("PlaybackActivityWidget", () => {
  beforeEach(() => {
    mocks.useAdminPlaybackActivity.mockReset();
  });

  it("renders one column per hour of the window", () => {
    mocks.useAdminPlaybackActivity.mockReturnValue({
      data: activity({
        buckets: [
          { hour: new Date(Date.now() - HOUR_MS).toISOString(), direct: 2, remux: 0, transcode: 1 },
        ],
      }),
      isLoading: false,
      error: null,
    });

    render(<PlaybackActivityWidget />);

    const chart = screen.getByRole("img", { name: /playback sessions per hour/i });
    // 25 hourly columns: the hour the window opens in is partial but real.
    expect(chart.querySelectorAll(":scope > div")).toHaveLength(25);
    expect(screen.getByText("3 sessions")).toBeTruthy();
    expect(screen.getByText("Direct stream")).toBeTruthy();
    expect(screen.getByText("Playback activity · last 24 h")).toBeTruthy();
  });

  // A month asks for 720 hours and gets daily buckets back, so the chart has to
  // draw 31 columns rather than 720 near-empty hourly ones.
  it("renders one column per day when the server bucketed daily", () => {
    mocks.useAdminPlaybackActivity.mockReturnValue({
      data: activity({
        hours: 720,
        bucket_seconds: 86_400,
        buckets: [
          { hour: new Date(Date.now() - DAY_MS).toISOString(), direct: 4, remux: 0, transcode: 2 },
        ],
      }),
      isLoading: false,
      error: null,
    });

    render(
      <WidgetChromeProvider id="playback-24h" range="month" setRange={() => {}}>
        <PlaybackActivityWidget />
      </WidgetChromeProvider>,
    );

    expect(mocks.useAdminPlaybackActivity).toHaveBeenCalledWith(720);
    const chart = screen.getByRole("img", { name: /playback sessions per day/i });
    expect(chart.querySelectorAll(":scope > div")).toHaveLength(31);
    expect(screen.getByText("6 sessions")).toBeTruthy();
    expect(screen.getByText("Playback activity · last 30 d")).toBeTruthy();
  });

  it("says the window was quiet instead of drawing an empty axis", () => {
    mocks.useAdminPlaybackActivity.mockReturnValue({
      data: activity(),
      isLoading: false,
      error: null,
    });

    render(<PlaybackActivityWidget />);

    expect(screen.getByText("No playback in the last 24 hours")).toBeTruthy();
  });

  it("surfaces a failed load", () => {
    mocks.useAdminPlaybackActivity.mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new Error("boom"),
    });

    render(<PlaybackActivityWidget />);

    expect(screen.getByText("Failed to load playback activity.")).toBeTruthy();
  });
});
