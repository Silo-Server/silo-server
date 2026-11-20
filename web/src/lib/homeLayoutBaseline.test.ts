import { describe, expect, it } from "vitest";
import { planPageHideCandidates, type ProfileSectionBaseline } from "./homeLayoutBaseline";

function baseline(overrides: Partial<ProfileSectionBaseline> = {}): ProfileSectionBaseline {
  return {
    section_tokens: ["tok-continue", "tok-collections", "tok-disabled"],
    active_section_tokens: ["tok-continue", "tok-collections"],
    visible_sections: [
      {
        id: "default-continue-watching",
        section_type: "continue_watching",
        title: "Continue Watching",
        token: "tok-continue",
      },
      {
        id: "s-collections",
        section_type: "collection_posters",
        title: "Collections",
        token: "tok-collections",
      },
    ],
    ...overrides,
  };
}

describe("planPageHideCandidates", () => {
  it("prechecks only rows absent from a valid baseline, ignoring disabled rows that existed at export", () => {
    const result = planPageHideCandidates(
      {
        overrides: [],
        serverSectionTokens: ["tok-continue", "tok-disabled"],
      },
      baseline({
        // Disabled row re-enabled later is already in serverSectionTokens, so it
        // isn't flagged as added since export.
        active_section_tokens: ["tok-continue", "tok-collections", "tok-disabled"],
        visible_sections: [
          ...baseline().visible_sections,
          {
            id: "s-reenabled",
            section_type: "hidden_gems",
            title: "Hidden Gems",
            token: "tok-disabled",
          },
        ],
      }),
    );

    expect(result.mode).toBe("baseline");
    expect(result.defaultChecked).toBe(true);
    expect(result.candidates.map((row) => row.id)).toEqual(["s-collections"]);
  });

  it("requires overlap with active_section_tokens before prechecking any row", () => {
    // Only a disabled row's token overlaps; active rows are all fallback defaults.
    const result = planPageHideCandidates(
      {
        overrides: [],
        serverSectionTokens: ["tok-disabled"],
      },
      baseline(),
    );

    expect(result.mode).toBe("unmatched");
    expect(result.defaultChecked).toBe(false);
    expect(result.candidates.map((row) => row.id)).toEqual([
      "default-continue-watching",
      "s-collections",
    ]);
  });

  it("treats legacy v1 pages without serverSectionTokens as unchecked per-row choices", () => {
    const result = planPageHideCandidates(
      {
        overrides: [{ section_id: "default-continue-watching" }],
      },
      baseline(),
    );

    expect(result.mode).toBe("legacy");
    expect(result.defaultChecked).toBe(false);
    expect(result.candidates.map((row) => row.id)).toEqual(["s-collections"]);
  });

  it("treats an empty or malformed baseline token list as unmatched and unchecked", () => {
    const result = planPageHideCandidates({ overrides: [], serverSectionTokens: [] }, baseline());

    expect(result.mode).toBe("unmatched");
    expect(result.defaultChecked).toBe(false);
    expect(result.candidates.map((row) => row.id)).toEqual([
      "default-continue-watching",
      "s-collections",
    ]);
  });
});
