import { describe, expect, it } from "vitest";
import { t } from "@/i18n";
import { externalProgress, externalRunLines } from "./adminRecommendationsExternal";

describe("adminRecommendationsExternal", () => {
  it("hides misleading 0/0/0 counts on retention-only runs while disabled", () => {
    expect(
      externalRunLines(t, {
        status: "completed",
        started_at: "2026-01-01T00:00:00Z",
        completed_at: "2026-01-01T00:00:01Z",
        result: { enabled: false, pruned_titles: 0 },
      }),
    ).toEqual([]);

    const pruned = externalRunLines(t, {
      status: "completed",
      started_at: "2026-01-01T00:00:00Z",
      completed_at: "2026-01-01T00:00:01Z",
      result: { enabled: false, pruned_titles: 4 },
    });
    expect(pruned).toEqual([{ text: "Cleared 4 expired titles" }]);
  });

  it("maps structured skip reasons to localized warning lines", () => {
    const lines = externalRunLines(t, {
      status: "completed",
      started_at: "2026-01-01T00:00:00Z",
      completed_at: "2026-01-01T00:00:01Z",
      result: {
        enabled: true,
        details_fetched: 0,
        tmdb_requests: 0,
        vectors_skipped: "no embedding lock yet",
        vectors_skipped_reason: "no_lock",
      },
    });
    expect(lines[1]).toEqual({
      text: "Run the Embeddings job first so Silo has library vectors to compare against.",
      tone: "warning",
    });
  });

  it("formats live phase progress only while running with a positive total", () => {
    expect(
      externalProgress(t, {
        enabled: true,
        running: false,
        count: 0,
        rows: 0,
        phase: "titles",
        progress_done: 10,
        progress_total: 100,
      }),
    ).toBeUndefined();

    expect(
      externalProgress(t, {
        enabled: true,
        running: true,
        count: 0,
        rows: 0,
        phase: "titles",
        progress_done: 120,
        progress_total: 100,
      }),
    ).toEqual({
      done: 100,
      total: 100,
      label: "Looking up and embedding titles (100 / 100)",
    });
  });
});
