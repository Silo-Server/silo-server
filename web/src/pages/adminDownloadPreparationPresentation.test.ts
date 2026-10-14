// @vitest-environment node

import { describe, expect, it } from "vitest";
import { makePreparation } from "@/test/downloadPreparations";
import {
  cancelPreparationsPrompt,
  formatRemaining,
  formatSpeed,
  preparationPercent,
  preparationRemainingSeconds,
  summarizePreparationAction,
} from "./adminDownloadPreparationPresentation";

describe("download preparation presentation", () => {
  it("derives percent and time left from the latest reading", () => {
    const prep = makePreparation();
    expect(preparationPercent(prep)).toBe(25);
    // 4500 s of media left at 2× realtime.
    expect(preparationRemainingSeconds(prep)).toBe(2250);
    expect(formatRemaining(2250)).toBe("about 38 min left");
    expect(formatRemaining(30)).toBe("under 1 min left");
    expect(formatRemaining(2 * 3600 + 5 * 60)).toBe("about 2 h 5 min left");
    expect(formatSpeed(3.44)).toBe("3.4×");
    expect(formatSpeed(41.2)).toBe("41×");

    const unknown = makePreparation({ progress: undefined });
    expect(preparationPercent(unknown)).toBeNull();
    expect(preparationRemainingSeconds(unknown)).toBeNull();
    expect(formatRemaining(null)).toBe("");
    const stalled = makePreparation({
      progress: { encoded_seconds: 10, duration_seconds: 6000, speed: 0, updated_at: "x" },
    });
    expect(preparationRemainingSeconds(stalled)).toBeNull();
    const over = makePreparation({
      progress: { encoded_seconds: 7000, duration_seconds: 6000, speed: 1, updated_at: "x" },
    });
    expect(preparationPercent(over)).toBe(100);
  });

  it("summarizes each outcome of an action", () => {
    expect(
      summarizePreparationAction("pause", [
        { id: "a", outcome: "applied" },
        { id: "b", outcome: "applied" },
        { id: "c", outcome: "unchanged" },
        { id: "d", outcome: "not_applicable" },
        { id: "e", outcome: "not_found" },
      ]),
    ).toBe(
      "Paused 2 jobs. 1 job was already paused. Skipped 1 job that failed. 1 job had already finished or been canceled.",
    );
    expect(
      summarizePreparationAction("resume", [
        { id: "a", outcome: "unchanged" },
        { id: "b", outcome: "unchanged" },
      ]),
    ).toBe("2 jobs weren't paused.");
    expect(summarizePreparationAction("cancel", [{ id: "a", outcome: "applied" }])).toBe(
      "Canceled 1 job.",
    );
    expect(summarizePreparationAction("cancel", [])).toBe("Nothing changed.");
  });

  it("names what canceling costs the requesters", () => {
    const running = makePreparation();
    expect(cancelPreparationsPrompt([running])).toEqual({
      title: "Cancel preparing Example Movie?",
      description:
        'Encoding stops and its progress is lost. 1 waiting download fails with "Canceled by an administrator". Users can download again later, which starts a new job.',
      confirmLabel: "Cancel job",
    });
    const failed = makePreparation({
      id: "f",
      state: "failed",
      progress: undefined,
      requesters: [],
    });
    expect(cancelPreparationsPrompt([failed])).toEqual({
      title: "Remove the failed job for Example Movie?",
      description: "No downloads are waiting on this job.",
      confirmLabel: "Remove",
    });
    const queued = makePreparation({ id: "q", state: "queued", progress: undefined });
    expect(cancelPreparationsPrompt([queued, failed]).title).toBe("Cancel 2 jobs?");
    expect(cancelPreparationsPrompt([queued, failed]).confirmLabel).toBe("Cancel 2 jobs");
  });
});
