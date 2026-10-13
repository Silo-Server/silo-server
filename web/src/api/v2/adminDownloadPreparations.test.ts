import { describe, expect, it } from "vitest";
import { makePreparation, makePreparationList } from "@/test/downloadPreparations";
import {
  applyDownloadPreparationProgress,
  isDownloadPreparationProgressEvent,
} from "./adminDownloadPreparations";

const progress = { encoded_seconds: 3000, duration_seconds: 6000, speed: 2.5, updated_at: "t" };

describe("download preparation progress events", () => {
  it("validates the event payload", () => {
    expect(isDownloadPreparationProgressEvent({ id: "art-1", progress })).toBe(true);
    expect(isDownloadPreparationProgressEvent({ id: "art-1" })).toBe(false);
    expect(
      isDownloadPreparationProgressEvent({ id: "art-1", progress: { ...progress, speed: "2" } }),
    ).toBe(false);
    expect(isDownloadPreparationProgressEvent(null)).toBe(false);
  });

  it("patches a running job and leaves the rest untouched", () => {
    const other = makePreparation({ id: "art-2", state: "queued", progress: undefined });
    const list = makePreparationList([
      makePreparation({ progress_unavailable: true, progress: undefined }),
      other,
    ]);
    const next = applyDownloadPreparationProgress(list, { id: "art-1", progress });
    expect(next?.items[0]?.progress).toEqual(progress);
    expect(next?.items[0]?.progress_unavailable).toBe(false);
    expect(next?.items[1]).toBe(other);
    expect(next?.counts).toBe(list.counts);
    expect(list.items[0]?.progress).toBeUndefined();
  });

  it("asks for a re-read when the job is missing or no longer running", () => {
    const list = makePreparationList([makePreparation({ state: "queued" })]);
    expect(applyDownloadPreparationProgress(list, { id: "art-1", progress })).toBeNull();
    expect(applyDownloadPreparationProgress(list, { id: "unknown", progress })).toBeNull();
  });
});
