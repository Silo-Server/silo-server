import { describe, expect, it } from "vitest";
import { rowsAddedSinceExport, type ProfileSectionBaseline } from "./homeLayoutBaseline";

function baseline(overrides: Partial<ProfileSectionBaseline> = {}): ProfileSectionBaseline {
  return {
    section_tokens: ["tok-continue", "tok-collections", "tok-disabled"],
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

function ids(rows: { id: string }[]): string[] {
  return rows.map((row) => row.id);
}

describe("rowsAddedSinceExport", () => {
  it("offers rows absent at export, not a disabled row that existed then and was re-enabled", () => {
    const rows = rowsAddedSinceExport(
      { overrides: [], serverSectionTokens: ["tok-continue", "tok-disabled"] },
      baseline({
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

    expect(ids(rows)).toEqual(["s-collections"]);
  });

  it("offers nothing when no row the profile sees matches the export", () => {
    // Only a row the profile can't see now matched, so the export can't
    // describe this page.
    expect(
      rowsAddedSinceExport({ overrides: [], serverSectionTokens: ["tok-disabled"] }, baseline()),
    ).toEqual([]);
  });

  it("offers nothing for a file without tokens or with an empty list", () => {
    expect(
      rowsAddedSinceExport(
        { overrides: [{ section_id: "default-continue-watching" }] },
        baseline(),
      ),
    ).toEqual([]);
    expect(rowsAddedSinceExport({ overrides: [], serverSectionTokens: [] }, baseline())).toEqual(
      [],
    );
  });

  it("leaves out a new row the file already has a change for", () => {
    const rows = rowsAddedSinceExport(
      { overrides: [{ section_id: "s-collections" }], serverSectionTokens: ["tok-continue"] },
      baseline(),
    );

    expect(rows).toEqual([]);
  });
});
