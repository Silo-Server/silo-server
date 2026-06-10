import { describe, expect, it } from "vitest";

import type { GuidedFormState } from "@/components/collections/CollectionGuidedRulesEditor";
import { getActiveFilterBadges } from "./catalogFilterBadges";

function state(overrides: Partial<GuidedFormState> = {}): GuidedFormState {
  return {
    mediaScope: "all",
    libraryIds: [],
    genres: [],
    decade: "",
    yearFrom: "",
    yearTo: "",
    minRating: "",
    contentRating: "",
    originalLanguages: [],
    actor: "",
    director: "",
    writer: "",
    producer: "",
    author: "",
    narrator: "",
    series: "",
    studio: "",
    network: "",
    country: "",
    status: "",
    watchStatus: "",
    addedInLast: "",
    releasedInLast: "",
    fourK: false,
    hdr: false,
    dolbyVision: false,
    sortField: "title",
    sortOrder: "asc",
    ...overrides,
  };
}

describe("catalogFilterBadges", () => {
  it("only shows narrator badges for audiobook scope", () => {
    expect(
      getActiveFilterBadges(state({ mediaScope: "ebook", narrator: "Should Not Apply" })).some(
        (badge) => badge.key === "narrator",
      ),
    ).toBe(false);

    expect(
      getActiveFilterBadges(state({ mediaScope: "audiobook", narrator: "Michael Kramer" })),
    ).toContainEqual({
      key: "narrator",
      label: "Narrator: Michael Kramer",
      clearPatch: { narrator: "" },
    });
  });

  it("labels progress status as read for ebook scope", () => {
    expect(
      getActiveFilterBadges(state({ mediaScope: "ebook", watchStatus: "in_progress" })),
    ).toContainEqual({
      key: "watchStatus",
      label: "Read: in progress",
      clearPatch: { watchStatus: "" },
    });

    expect(
      getActiveFilterBadges(state({ mediaScope: "movie", watchStatus: "in_progress" })),
    ).toContainEqual({
      key: "watchStatus",
      label: "Watch: in progress",
      clearPatch: { watchStatus: "" },
    });
  });

  it("uses listening copy for audiobook watch-status badges", () => {
    expect(
      getActiveFilterBadges(state({ mediaScope: "audiobook", watchStatus: "watched" }), {
        isAudiobookLibrary: true,
      }),
    ).toContainEqual(expect.objectContaining({ key: "watchStatus", label: "Listening: listened" }));

    expect(
      getActiveFilterBadges(state({ mediaScope: "audiobook", watchStatus: "unwatched" }), {
        isAudiobookLibrary: true,
      }),
    ).toContainEqual(
      expect.objectContaining({ key: "watchStatus", label: "Listening: unlistened" }),
    );
  });

  it("keeps watch copy for non-audiobook badges", () => {
    expect(getActiveFilterBadges(state({ watchStatus: "watched" }))).toContainEqual(
      expect.objectContaining({ key: "watchStatus", label: "Watch: watched" }),
    );
  });
});
