import { QueryClient, QueryClientProvider, QueryObserver } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import CardOverlays from "@/components/overlays/CardOverlays";
import { watchCountKeys } from "@/hooks/queries/keys";
import { refreshUnwatchedEpisodeCount } from "@/hooks/queries/watchCounts";
import { buildDefaultPrefs, type CardOverlayPrefs, type OverlayId } from "@/lib/overlays";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
installV2Recorder();

const WATCH_COUNTS = "POST /api/v2/catalog/watch-counts";

function prefsWithOnly(id: OverlayId | null): CardOverlayPrefs {
  const prefs = buildDefaultPrefs();
  for (const key of Object.keys(prefs.items) as OverlayId[]) {
    prefs.items[key] = { ...prefs.items[key], enabled: key === id };
  }
  return prefs;
}

function renderCards(prefs: CardOverlayPrefs, seriesIds: (string | undefined)[]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={client}>
      {seriesIds.map((id, index) => (
        <div key={index} data-testid={`card-${index}`}>
          <CardOverlays data={{ unwatched_episodes_series_id: id }} prefs={prefs} />
        </div>
      ))}
    </QueryClientProvider>,
  );
  const badges = (index: number) =>
    Array.from(view.getByTestId(`card-${index}`).querySelectorAll("[data-overlay-badge]")).map(
      (badge) => badge.textContent,
    );
  return { client, badges };
}

describe("unwatched-episodes badge", () => {
  beforeEach(() => vi.stubGlobal("CSS", { supports: () => true }));
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("counts every series card on screen in one request and hides a finished series", async () => {
    // The fixture answers Severance with 8 left and The Office finished.
    const { badges } = renderCards(prefsWithOnly("unwatched_episodes"), [
      "series:severance",
      "series:the-office",
      undefined,
    ]);

    await waitFor(() => expect(badges(0)).toEqual(["8"]));
    expect(badges(1)).toEqual([]);
    expect(badges(2)).toEqual([]);
    expect(v2Recorder.callsOf(WATCH_COUNTS).map((call) => call.body)).toEqual([
      { series_ids: ["series:severance", "series:the-office"] },
    ]);
  });

  it("asks for nothing while the badge is off", async () => {
    renderCards(prefsWithOnly(null), ["series:severance"]);
    await act(() => new Promise((resolve) => setTimeout(resolve, 10)));
    expect(v2Recorder.callsOf(WATCH_COUNTS)).toEqual([]);
  });

  it("refetches a watched mark at once and progress once per window", async () => {
    vi.useFakeTimers();
    const client = new QueryClient();
    const queryKey = watchCountKeys.series("series:severance");
    const queryFn = vi.fn(async () => 8);
    const unsubscribe = new QueryObserver(client, { queryKey, queryFn }).subscribe(() => {});
    await vi.advanceTimersByTimeAsync(0);
    expect(queryFn).toHaveBeenCalledTimes(1);

    // Progress ticks mark the count stale for the next card that mounts and
    // refetch the one on screen once the window ends.
    refreshUnwatchedEpisodeCount(client, "series:severance", "progress");
    refreshUnwatchedEpisodeCount(client, "series:severance", "progress");
    await vi.advanceTimersByTimeAsync(0);
    expect(client.getQueryState(queryKey)?.isInvalidated).toBe(true);
    expect(queryFn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(queryFn).toHaveBeenCalledTimes(2);

    refreshUnwatchedEpisodeCount(client, "series:severance", "watched");
    await vi.advanceTimersByTimeAsync(0);
    expect(queryFn).toHaveBeenCalledTimes(3);

    // A series no card counted (or a movie's ID) schedules nothing.
    const timers = vi.getTimerCount();
    refreshUnwatchedEpisodeCount(client, "movie:heat-1995", "progress");
    expect(vi.getTimerCount()).toBe(timers);
    unsubscribe();
  });
});
