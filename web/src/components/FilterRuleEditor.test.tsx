import { render, screen, within } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import {
  COLLECTION_FIELD_OPTIONS,
  COLLECTION_SORT_OPTIONS,
  getCollectionSortOptions,
  getCollectionFieldOption,
} from "@/components/collections/collectionBuilderFields";

import FilterRuleEditor from "./FilterRuleEditor";
import { getFilterRuleFieldOptions } from "./FilterRuleEditor";

vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

describe("FilterRuleEditor", () => {
  it("renders rule-management controls as non-submit buttons", () => {
    const markup = renderToStaticMarkup(
      <form>
        <FilterRuleEditor
          value={{
            match: "all",
            groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "" }] }],
          }}
          onChange={() => {}}
        />
      </form>,
    );

    const buttons = [...markup.matchAll(/<button\b[^>]*>/g)].map((match) => match[0]);

    expect(buttons.length).toBeGreaterThan(0);
    expect(buttons.every((button) => button.includes('type="button"'))).toBe(true);
    expect(markup).not.toContain('type="submit"');
  });

  it("exposes canonical shared field and sort vocabulary", () => {
    expect(COLLECTION_FIELD_OPTIONS.map((field) => field.value)).toEqual(
      expect.arrayContaining([
        "actor",
        "writer",
        "producer",
        "in_progress",
        "resolution",
        "hdr",
        "dolby_vision",
        "rating_imdb",
        "release_date",
        "watched",
        "favorited",
        "in_watchlist",
      ]),
    );
    expect(COLLECTION_SORT_OPTIONS.map((sort) => sort.value)).toContain("rating_imdb");
    expect(COLLECTION_SORT_OPTIONS.map((sort) => sort.value)).not.toContain("rating");
    expect(getCollectionSortOptions(false).map((sort) => sort.value)).not.toContain("progress");
  });

  it("describes range and boolean editing for shared rule fields", () => {
    expect(getCollectionFieldOption("rating_imdb")).toMatchObject({
      inputType: "number",
      supportsRange: true,
    });
    expect(getCollectionFieldOption("release_date")).toMatchObject({
      inputType: "date",
      supportsRange: true,
    });
    expect(getCollectionFieldOption("actor")).toMatchObject({
      inputType: "person_search",
    });
    expect(getCollectionFieldOption("watched")).toMatchObject({
      inputType: "boolean",
      valueType: "boolean",
      personalized: true,
    });
    expect(getCollectionFieldOption("resolution")).toMatchObject({
      inputType: "select",
      selectOptions: expect.arrayContaining([{ value: "2160p", label: "2160p" }]),
    });
  });

  it("labels watched fields as read fields for ebook scope", () => {
    const ebookOptions = getFilterRuleFieldOptions(true, "ebook");
    const movieOptions = getFilterRuleFieldOptions(true, "movie");

    expect(ebookOptions.find((option) => option.value === "watched")?.label).toBe("Read");
    expect(ebookOptions.find((option) => option.value === "in_progress")?.label).toBe(
      "In progress",
    );
    expect(movieOptions.find((option) => option.value === "watched")?.label).toBe("Watched");
  });

  it("names each rule's controls by the rule's number", () => {
    render(
      <FilterRuleEditor
        value={{
          match: "all",
          groups: [
            {
              match: "all",
              rules: [
                { field: "genre", op: "is", value: "Drama" },
                { field: "rating_imdb", op: "gte", value: 7 },
              ],
            },
          ],
        }}
        onChange={() => {}}
      />,
    );

    const second = screen.getByRole("group", { name: "Rule 2" });
    expect(within(second).getByRole("combobox", { name: "Field" })).toHaveTextContent(
      "IMDb rating",
    );
    expect(within(second).getByRole("button", { name: "Remove rule" })).toBeInTheDocument();
  });

  it("shows rules its controls cannot represent as read-only rules", () => {
    render(
      <FilterRuleEditor
        value={{
          match: "all",
          groups: [
            {
              match: "all",
              rules: [
                { field: "genre", op: "is", value: "Drama" },
                { field: "year", op: "contains", value: 1999 },
                { field: "year", op: "between", value: "1990-1999" },
                { field: "hdr", op: "is", value: "true" },
                { field: "in_watchlist", op: "is", value: true },
              ],
            },
          ],
        }}
        onChange={() => {}}
      />,
    );

    expect(
      screen
        .getAllByRole("group", { name: "Rule not editable here" })
        .map((rule) => rule.textContent),
    ).toEqual([
      "Not editable here year contains 1999Remove",
      'Not editable here year between "1990-1999"Remove',
      'Not editable here hdr is "true"Remove',
      "Not editable here in_watchlist is trueRemove",
    ]);
  });

  // The guided editor writes fields the server accepts but these controls do
  // not offer, so the read-only label must not call them broken.
  it("does not call valid guided-editor rules unsupported", () => {
    render(
      <FilterRuleEditor
        value={{
          match: "all",
          groups: [
            {
              match: "all",
              rules: [
                { field: "original_language", op: "is", value: "fr" },
                { field: "author", op: "is", value: "Ursula K. Le Guin" },
              ],
            },
          ],
        }}
        onChange={() => {}}
      />,
    );

    expect(screen.queryByText(/unsupported/i)).toBeNull();
    expect(
      screen
        .getAllByRole("group", { name: "Rule not editable here" })
        .map((rule) => rule.textContent),
    ).toEqual([
      'Not editable here original_language is "fr"Remove',
      'Not editable here author is "Ursula K. Le Guin"Remove',
    ]);
  });
});
