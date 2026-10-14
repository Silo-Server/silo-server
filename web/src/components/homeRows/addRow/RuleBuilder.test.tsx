import { useState } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { QueryDefinition } from "@/api/types";
import { RuleBuilder, RuleSortField } from "./RuleBuilder";

vi.mock("@/hooks/queries/personSearch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/personSearch")>()),
  useExtendedQueryRules: () => true,
}));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

beforeEach(() => {
  latest = undefined;
});

beforeAll(() => {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});

const libraries = [
  { id: 1, name: "Movies" },
  { id: 2, name: "TV" },
];

function query(overrides: Partial<QueryDefinition> = {}): QueryDefinition {
  return {
    library_ids: [],
    media_scope: "movie",
    match: "all",
    groups: [
      {
        match: "all",
        rules: [
          { field: "year", op: "between", value: [1990, 1999] },
          { field: "rating_imdb", op: "gte", value: 7 },
        ],
      },
    ],
    sort: { field: "rating_imdb", order: "desc" },
    ...overrides,
  };
}

let latest: QueryDefinition | undefined;

function Harness({ initial }: { initial: QueryDefinition }) {
  const [value, setValue] = useState(initial);
  const change = (next: QueryDefinition) => {
    latest = next;
    setValue(next);
  };
  return (
    <>
      <RuleBuilder value={value} libraries={libraries} allowPersonalized onChange={change} />
      <RuleSortField value={value} allowPersonalized onChange={change} />
    </>
  );
}

/** The sentence's words and control values, in reading order. */
function sentencePieces(sentence: HTMLElement): string[] {
  return Array.from(sentence.children)
    .filter((child) => child.getAttribute("aria-hidden") !== "true")
    .map((child) => child.textContent?.trim() ?? "");
}

function choose(combobox: HTMLElement, option: string) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  fireEvent.click(screen.getByRole("option", { name: option }));
}

describe("RuleBuilder", () => {
  it("reads as a sentence: show what, from which libraries, matching all or any", () => {
    render(<Harness initial={query()} />);
    const sentence = screen.getByRole("group", { name: "What the row shows" });
    expect(sentencePieces(sentence)).toEqual([
      "Show",
      "movies",
      "from",
      "Libraries: all libraries",
      "that match",
      "all",
      "of these:",
    ]);
    expect(within(sentence).getByRole("combobox", { name: "Kind of titles" })).toBeInTheDocument();
    expect(
      within(sentence).getByRole("combobox", { name: "How the rules combine" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("combobox", { name: "Field" })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Easy" })).toBeNull();
  });

  it("names one chosen library as “the … library”", () => {
    render(<Harness initial={query({ library_ids: [1] })} />);
    expect(sentencePieces(screen.getByRole("group", { name: "What the row shows" }))).toEqual([
      "Show",
      "movies",
      "from the",
      "Libraries: Movies",
      "library",
      "that match",
      "all",
      "of these:",
    ]);
  });

  it("names several chosen libraries as “the … libraries”", () => {
    render(<Harness initial={query({ library_ids: [1, 2], groups: [] })} />);
    expect(sentencePieces(screen.getByRole("group", { name: "What the row shows" }))).toEqual([
      "Show",
      "movies",
      "from the",
      "Libraries: Movies, TV",
      "libraries",
    ]);
  });

  it("doesn't repeat “libraries” while the chosen libraries' names are unknown", () => {
    render(<Harness initial={query({ library_ids: [8, 9] })} />);
    expect(
      sentencePieces(screen.getByRole("group", { name: "What the row shows" })).slice(2, 5),
    ).toEqual(["from the", "Libraries: 2 libraries", "that match"]);
  });

  it("reads one chosen library whose name is unknown as “1 library”", () => {
    render(<Harness initial={query({ library_ids: [9] })} />);
    expect(
      sentencePieces(screen.getByRole("group", { name: "What the row shows" })).slice(2, 5),
    ).toEqual(["from the", "Libraries: 1 library", "that match"]);
  });

  it("counts chosen libraries whose names are unknown alongside the named ones", () => {
    render(<Harness initial={query({ library_ids: [1, 9] })} />);
    expect(
      sentencePieces(screen.getByRole("group", { name: "What the row shows" })).slice(2, 5),
    ).toEqual(["from the", "Libraries: Movies +1 more", "libraries"]);
  });

  it("starts “that match …” on a second line, as the mockup lays it out", () => {
    render(<Harness initial={query({ library_ids: [1, 2] })} />);
    const sentence = screen.getByRole("group", { name: "What the row shows" });
    const clause = within(sentence).getByText("that match");
    const lineBreak = clause.previousElementSibling;
    expect(lineBreak).toHaveAttribute("aria-hidden", "true");
    expect(lineBreak).toHaveClass("basis-full");
    expect(lineBreak).toBeEmptyDOMElement();
  });

  it("keeps the picker's reset item capitalized while the sentence reads “all libraries”", async () => {
    const { unmount } = render(<Harness initial={query({ library_ids: [1] })} />);
    await userEvent.click(screen.getByRole("button", { name: /^Libraries:\s*Movies$/ }));
    expect(screen.getByRole("menuitem", { name: "All libraries" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "All libraries" }));
    expect(latest?.library_ids).toEqual([]);
    unmount();

    render(<Harness initial={query()} />);
    expect(
      screen.getByRole("button", { name: /^Libraries:\s*all libraries$/ }),
    ).toBeInTheDocument();
  });

  it("offers movies and shows together as one kind, and shows it when stored", () => {
    const { unmount } = render(<Harness initial={query()} />);
    choose(screen.getByRole("combobox", { name: "Kind of titles" }), "movies and shows");
    expect(latest?.media_scope).toBe("video");
    unmount();

    render(<Harness initial={query({ media_scope: "video" })} />);
    expect(screen.getByRole("combobox", { name: "Kind of titles" })).toHaveTextContent(
      "movies and shows",
    );
  });

  it("changes how the first group's rules combine from the sentence", () => {
    render(<Harness initial={query()} />);
    choose(screen.getByRole("combobox", { name: "How the rules combine" }), "any");
    expect(latest!.groups[0]!.match).toBe("any");
    expect(latest!.match).toBe("all");
  });

  it("adds a rule to the group, and starts the first group when there are no rules", async () => {
    render(<Harness initial={query({ groups: [] })} />);
    expect(screen.getByText(/No rules yet/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Add rule" }));
    expect(latest!.groups).toEqual([
      { match: "all", rules: [{ field: "genre", op: "is", value: "" }] },
    ]);
    await userEvent.click(screen.getByRole("button", { name: "Add rule" }));
    expect(latest!.groups[0]!.rules).toHaveLength(2);
  });

  it("moves focus to the value of the rule just added", async () => {
    render(<Harness initial={query()} />);
    await userEvent.click(screen.getByRole("button", { name: "Add rule" }));
    const third = screen.getByRole("group", { name: "Rule 3" });
    expect(within(third).getByRole("combobox", { name: "Value" })).toHaveFocus();
    expect(within(third).getByRole("combobox", { name: "Value" })).toHaveTextContent(
      "Pick a genre",
    );

    await userEvent.click(screen.getByRole("button", { name: "Add an “or” group" }));
    const group = screen.getByRole("group", { name: "Group 2" });
    expect(within(group).getByRole("combobox", { name: "Value" })).toHaveFocus();
  });

  it("adds an “or” group: titles match the first group or the new one", async () => {
    render(<Harness initial={query()} />);
    await userEvent.click(screen.getByRole("button", { name: "Add an “or” group" }));
    expect(latest!.match).toBe("any");
    expect(latest!.groups).toHaveLength(2);
    expect(latest!.groups[0]).toEqual(query().groups[0]);
    const second = screen.getByRole("group", { name: "Group 2" });
    expect(
      within(second).getByRole("combobox", { name: "How the groups combine" }),
    ).toHaveTextContent("or");
    await userEvent.click(within(second).getByRole("button", { name: "Add rule" }));
    expect(latest!.groups[1]!.rules).toHaveLength(2);
  });

  it("shows a stored multi-group filter as it is, and changes nothing until edited", () => {
    const stored = query({
      match: "all",
      groups: [
        { match: "all", rules: [{ field: "genre", op: "is", value: "Horror" }] },
        { match: "any", rules: [{ field: "genre", op: "is_not", value: "Comedy" }] },
      ],
    });
    render(<Harness initial={stored} />);
    expect(latest).toBeUndefined();
    const second = screen.getByRole("group", { name: "Group 2" });
    expect(
      within(second).getByRole("combobox", { name: "How the groups combine" }),
    ).toHaveTextContent("and");
    expect(
      within(second).getByRole("combobox", { name: "How group 2's rules combine" }),
    ).toHaveTextContent("any");
    // A group joined with "and" adds another "and" group, so the stored logic never flips.
    expect(screen.getByRole("button", { name: "Add an “and” group" })).toBeInTheDocument();
  });

  it("names each rule's controls by the rule's number", async () => {
    render(<Harness initial={query()} />);
    const second = screen.getByRole("group", { name: "Rule 2" });
    expect(within(second).getByRole("combobox", { name: "Field" })).toHaveTextContent(
      "IMDb rating",
    );
    await userEvent.click(within(second).getByRole("button", { name: "Remove rule" }));
    expect(latest!.groups[0]!.rules).toEqual([
      { field: "year", op: "between", value: [1990, 1999] },
    ]);
  });

  it("drops a group when its last rule is removed", async () => {
    render(
      <Harness
        initial={query({
          match: "any",
          groups: [
            query().groups[0]!,
            { match: "all", rules: [{ field: "genre", op: "is", value: "Drama" }] },
          ],
        })}
      />,
    );
    const second = screen.getByRole("group", { name: "Group 2" });
    await userEvent.click(within(second).getByRole("button", { name: "Remove rule" }));
    expect(latest!.groups).toHaveLength(1);
  });

  it("keeps rules it can't edit read-only, and personalized rules editable", () => {
    render(
      <Harness
        initial={query({
          groups: [
            {
              match: "all",
              rules: [
                { field: "cast", op: "contains", value: "Tom Hanks" },
                { field: "watched", op: "is", value: false },
              ],
            },
          ],
        })}
      />,
    );
    expect(screen.getByRole("group", { name: "Rule not editable here" })).toHaveTextContent(
      'cast contains "Tom Hanks"',
    );
    expect(screen.getByRole("combobox", { name: "Field" })).toHaveTextContent("Watched");
  });

  it("orders the row from More options, including personalized orders", () => {
    render(<Harness initial={query({ sort: { field: "date_viewed", order: "desc" } })} />);
    expect(screen.getByRole("combobox", { name: "Sort by" })).toHaveTextContent("Date Viewed");
    choose(screen.getByRole("combobox", { name: "Direction" }), "Ascending");
    expect(latest!.sort).toEqual({ field: "date_viewed", order: "asc" });
  });

  it("offers only the orders the row's kind of title supports", () => {
    render(
      <Harness
        initial={query({ media_scope: "ebook", sort: { field: "author", order: "asc" } })}
      />,
    );
    fireEvent.pointerDown(screen.getByRole("combobox", { name: "Sort by" }), {
      button: 0,
      ctrlKey: false,
      pointerType: "mouse",
    });
    expect(screen.getByRole("option", { name: "Author" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Narrator" })).toBeNull();
  });
});

describe("RuleBuilder in a Smart collection", () => {
  it("talks about the collection, not a row", () => {
    render(
      <RuleBuilder
        context="collection"
        value={query({ groups: [] })}
        libraries={libraries}
        onChange={() => {}}
      />,
    );
    expect(screen.getByRole("group", { name: "What the collection shows" })).toBeInTheDocument();
    expect(
      screen.getByText("No rules yet, so the collection holds every title from these libraries."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/the row/)).toBeNull();
  });

  it("offers no all-libraries choice when a library is required", async () => {
    render(
      <RuleBuilder
        context="collection"
        librariesRequired
        value={query({ library_ids: [1] })}
        libraries={libraries}
        onChange={() => {}}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /^Libraries:\s*Movies$/ }));
    expect(screen.queryByRole("menuitem", { name: "All libraries" })).toBeNull();
    expect(screen.getByRole("menuitemcheckbox", { name: "TV" })).toBeInTheDocument();
  });

  it("names every library the way the scope does", async () => {
    render(
      <RuleBuilder
        context="collection"
        allLibrariesLabel="all my libraries"
        value={query({ library_ids: [1] })}
        libraries={libraries}
        onChange={(next) => (latest = next)}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /^Libraries:\s*Movies$/ }));
    await userEvent.click(screen.getByRole("menuitem", { name: "All my libraries" }));
    expect(latest?.library_ids).toEqual([]);
  });
});
