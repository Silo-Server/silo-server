import { fireEvent, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import type { FilterConfig, FilterRule } from "@/api/types";

import FilterRuleEditor, { getFilterRuleFieldOptions } from "./FilterRuleEditor";

vi.mock("@/hooks/queries/personSearch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/personSearch")>()),
  useQueryRuleCapabilities: () => ({ extendedRules: true, showTitleRules: true }),
}));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

function renderRules(rules: FilterRule[], onChange: (value: FilterConfig) => void = () => {}) {
  return render(
    <FilterRuleEditor
      value={{ match: "all", groups: [{ match: "all", rules }] }}
      onChange={onChange}
    />,
  );
}

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

  it("offers show-title rules only for episodes on a server that supports them", () => {
    const fields = (scope: Parameters<typeof getFilterRuleFieldOptions>[1], supported: boolean) =>
      getFilterRuleFieldOptions(false, scope, undefined, { showTitleRules: supported });
    expect(fields("episode", true).map((field) => field.value)).toContain("show_title");
    expect(fields("episode", false).map((field) => field.value)).not.toContain("show_title");
    for (const scope of ["all", "video", "series", "movie", "ebook"] as const) {
      expect(fields(scope, true).map((field) => field.value)).not.toContain("show_title");
    }
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
    renderRules([{ field: "rating_rt_critic", op: "gte", value: 90 }]);
    expect(screen.queryByRole("group", { name: "Rule not editable here" })).toBeNull();
    expect(screen.getByRole("combobox", { name: "Field" })).toHaveTextContent("RT critic score");
    expect(screen.getByRole("spinbutton", { name: "Value" })).toHaveValue(90);
  });

  // The guided editor writes fields the server accepts but these controls do
  // not offer, so the read-only label must not call them broken.
  it.each<[string, FilterRule[], string[]]>([
    [
      "shows rules its controls cannot represent as read-only rules",
      [
        { field: "genre", op: "is", value: "Drama" },
        { field: "year", op: "contains", value: 1999 },
        { field: "year", op: "between", value: "1990-1999" },
        { field: "hdr", op: "is", value: "true" },
        { field: "in_watchlist", op: "is", value: true },
      ],
      [
        "Not editable here year contains 1999Remove",
        'Not editable here year between "1990-1999"Remove',
        'Not editable here hdr is "true"Remove',
        "Not editable here in_watchlist is trueRemove",
      ],
    ],
    [
      "does not call valid guided-editor rules unsupported",
      [
        { field: "narrator", op: "is", value: "Kobna Holdbrook-Smith" },
        { field: "author", op: "is", value: "Ursula K. Le Guin" },
      ],
      [
        'Not editable here narrator is "Kobna Holdbrook-Smith"Remove',
        'Not editable here author is "Ursula K. Le Guin"Remove',
      ],
    ],
  ])("%s", (_name, rules, readOnly) => {
    renderRules(rules);

    expect(screen.queryByText(/unsupported/i)).toBeNull();
    expect(
      screen
        .getAllByRole("group", { name: "Rule not editable here" })
        .map((rule) => rule.textContent),
    ).toEqual(readOnly);
  });

  it("keeps a cleared number value empty instead of writing zero", () => {
    const onChange = vi.fn();
    renderRules([{ field: "rating_imdb", op: "gte", value: 7 }], onChange);

    fireEvent.change(screen.getByRole("spinbutton", { name: "Value" }), {
      target: { value: "" },
    });

    expect(onChange).toHaveBeenLastCalledWith({
      match: "all",
      groups: [{ match: "all", rules: [{ field: "rating_imdb", op: "gte", value: "" }] }],
    });
  });
});
