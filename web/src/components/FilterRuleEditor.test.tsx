import { fireEvent, render, screen, within } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import {
  COLLECTION_FIELD_OPTIONS,
  getCollectionFieldOption,
  getCollectionSortOptions,
} from "@/components/collections/collectionBuilderFields";

import FilterRuleEditor, { getFilterRuleFieldOptions } from "./FilterRuleEditor";

vi.mock("@/hooks/queries/personSearch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/personSearch")>()),
  useExtendedQueryRules: () => true,
}));
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
    expect(getCollectionSortOptions().map((sort) => sort.value)).toContain("rating_imdb");
    expect(getCollectionSortOptions().map((sort) => sort.value)).not.toContain("rating");
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
    expect(ebookOptions.find((option) => option.value === "last_watched")?.label).toBe("Last read");
    expect(ebookOptions.find((option) => option.value === "in_progress")?.label).toBe(
      "In progress",
    );
    expect(movieOptions.find((option) => option.value === "watched")?.label).toBe("Watched");
  });

  it("offers fields about a show's episodes only where shows can match", () => {
    const values = (scope: Parameters<typeof getFilterRuleFieldOptions>[1]) =>
      getFilterRuleFieldOptions(false, scope).map((option) => option.value);
    for (const scope of ["all", "video", "series"] as const) {
      expect(values(scope)).toEqual(
        expect.arrayContaining(["latest_episode_added", "last_air_date"]),
      );
    }
    for (const scope of ["movie", "episode", "ebook"] as const) {
      expect(values(scope)).not.toContain("latest_episode_added");
      expect(values(scope)).not.toContain("last_air_date");
    }
  });

  it("leaves out the Rotten Tomatoes scores where only episodes match, since episodes have none", () => {
    const values = (scope: Parameters<typeof getFilterRuleFieldOptions>[1]) =>
      getFilterRuleFieldOptions(false, scope).map((option) => option.value);
    expect(values("episode")).not.toContain("rating_rt_critic");
    expect(values("episode")).not.toContain("rating_rt_audience");
    expect(values("episode")).toContain("rating_tmdb");
    expect(values("series")).toContain("rating_rt_critic");
  });

  it("offers a rating only while its source is shown", () => {
    const values = (sources?: ReadonlySet<string>) =>
      getFilterRuleFieldOptions(false, "movie", sources).map((option) => option.value);
    expect(values(new Set(["imdb", "tmdb"]))).not.toContain("rating_rt_critic");
    expect(values(new Set(["imdb", "tmdb", "rt_critic"]))).toEqual(
      expect.arrayContaining(["rating_imdb", "rating_tmdb", "rating_rt_critic"]),
    );
    expect(values(new Set(["imdb", "tmdb", "rt_critic"]))).not.toContain("rating_rt_audience");
    expect(values()).toEqual(expect.arrayContaining(["rating_rt_critic", "rating_rt_audience"]));
  });

  it("keeps a saved rule on a hidden rating editable", () => {
    render(
      <FilterRuleEditor
        value={{
          match: "all",
          groups: [{ match: "all", rules: [{ field: "rating_rt_critic", op: "gte", value: 90 }] }],
        }}
        onChange={() => {}}
      />,
    );
    expect(screen.queryByRole("group", { name: "Rule not editable here" })).toBeNull();
    expect(screen.getByRole("combobox", { name: "Field" })).toHaveTextContent("RT critic score");
    expect(screen.getByRole("spinbutton", { name: "Value" })).toHaveValue(90);
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
                { field: "narrator", op: "is", value: "Kobna Holdbrook-Smith" },
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
      'Not editable here narrator is "Kobna Holdbrook-Smith"Remove',
      'Not editable here author is "Ursula K. Le Guin"Remove',
    ]);
  });

  it("keeps a cleared number value empty instead of writing zero", () => {
    const onChange = vi.fn();
    render(
      <FilterRuleEditor
        value={{
          match: "all",
          groups: [{ match: "all", rules: [{ field: "rating_imdb", op: "gte", value: 7 }] }],
        }}
        onChange={onChange}
      />,
    );

    fireEvent.change(screen.getByRole("spinbutton", { name: "Value" }), {
      target: { value: "" },
    });

    expect(onChange).toHaveBeenLastCalledWith({
      match: "all",
      groups: [{ match: "all", rules: [{ field: "rating_imdb", op: "gte", value: "" }] }],
    });
  });
});
