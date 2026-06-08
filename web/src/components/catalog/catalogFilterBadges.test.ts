import { describe, expect, it } from "vitest";
import type { GuidedFormState } from "@/components/collections/CollectionGuidedRulesEditor";
import { getActiveFilterBadges } from "./catalogFilterBadges";

function state(watchStatus: GuidedFormState["watchStatus"]): GuidedFormState {
  return {
    mediaScope: "audiobook",
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
    watchStatus,
    addedInLast: "",
    releasedInLast: "",
    fourK: false,
    hdr: false,
    dolbyVision: false,
    sortField: "title",
    sortOrder: "asc",
  };
}

describe("getActiveFilterBadges", () => {
  it("uses listening copy for audiobook watch-status badges", () => {
    expect(getActiveFilterBadges(state("watched"), { isAudiobookLibrary: true })).toContainEqual(
      expect.objectContaining({ key: "watchStatus", label: "Listening: listened" }),
    );
    expect(getActiveFilterBadges(state("unwatched"), { isAudiobookLibrary: true })).toContainEqual(
      expect.objectContaining({ key: "watchStatus", label: "Listening: unlistened" }),
    );
  });

  it("keeps watch copy for non-audiobook badges", () => {
    expect(getActiveFilterBadges(state("watched"))).toContainEqual(
      expect.objectContaining({ key: "watchStatus", label: "Watch: watched" }),
    );
  });
});
