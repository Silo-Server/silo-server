import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import SectionEditorDrawer from "./SectionEditorDrawer";

vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({ collections: [], isLoading: false }),
}));

vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

const libraries = [
  { id: 1, name: "Movies" },
  { id: 2, name: "TV" },
];

// Configs in the shape the drawer has always written for custom-filter rows.
const multiGroupConfig = {
  library_ids: [1],
  match: "any",
  groups: [
    {
      match: "all",
      rules: [
        { field: "genre", op: "is", value: "Horror" },
        { field: "year", op: "gte", value: 1980 },
      ],
    },
    { match: "any", rules: [{ field: "genre", op: "is_not", value: "Comedy" }] },
  ],
  sort: { field: "year", order: "asc" },
};

// The removed Easy mode saved fields the server does not know and booleans as text.
const unknownFieldConfig = {
  library_ids: [],
  match: "all",
  groups: [
    {
      match: "all",
      rules: [
        { field: "cast", op: "contains", value: "Tom Hanks" },
        { field: "genre", op: "is", value: "Drama" },
        { field: "watched", op: "is", value: "true" },
      ],
    },
  ],
  sort: { field: "added_at", order: "desc" },
};

function renderAdmin(config: Record<string, unknown>, onSave = vi.fn()) {
  render(
    <SectionEditorDrawer
      mode="admin"
      open
      onOpenChange={() => {}}
      section={{
        id: "row-1",
        scope: "home",
        library_id: null,
        position: 0,
        section_type: "custom_filter",
        title: "Rule row",
        featured: false,
        item_limit: 20,
        enabled: true,
        created_at: "",
        updated_at: "",
        config,
      }}
      scope="home"
      currentLibraryId={null}
      libraries={libraries}
      onSave={onSave}
    />,
  );
  return onSave;
}

function renderProfile(config: Record<string, unknown>, onSave = vi.fn()) {
  render(
    <SectionEditorDrawer
      mode="profile"
      open
      onOpenChange={() => {}}
      section={{
        id: "row-1",
        section_type: "custom_filter",
        title: "Rule row",
        featured: false,
        item_limit: 20,
        hidden: false,
        is_custom: true,
        customized: false,
        position: 0,
        config,
      }}
      libraries={libraries}
      onSave={onSave}
    />,
  );
  return onSave;
}

describe("SectionEditorDrawer custom-filter rows", () => {
  it.each([
    ["admin", "multi-group", renderAdmin, multiGroupConfig],
    ["admin", "unknown-field", renderAdmin, unknownFieldConfig],
    ["profile", "multi-group", renderProfile, multiGroupConfig],
    ["profile", "unknown-field", renderProfile, unknownFieldConfig],
  ])("saves an untouched %s %s row with the config it opened with", async (_, __, open, config) => {
    const onSave = open(structuredClone(config));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(JSON.stringify(onSave.mock.calls[0]![0].config)).toBe(JSON.stringify(config));
  });

  it("edits rules in one editor with each scope field shown once", () => {
    renderAdmin(structuredClone(multiGroupConfig));
    expect(screen.queryByRole("button", { name: "Easy" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Advanced" })).toBeNull();
    expect(screen.getAllByText("Media Scope")).toHaveLength(1);
    expect(screen.getAllByText("Libraries")).toHaveLength(1);
    expect(screen.getAllByText("Rule Groups")).toHaveLength(1);
  });

  it("keeps personalized rules and sorts editable", () => {
    renderAdmin({
      library_ids: [],
      match: "all",
      groups: [{ match: "all", rules: [{ field: "watched", op: "is", value: false }] }],
      sort: { field: "date_viewed", order: "desc" },
    });
    expect(screen.queryByRole("group", { name: "Rule not editable here" })).toBeNull();
    const comboboxes = screen.getAllByRole("combobox").map((box) => box.textContent);
    expect(comboboxes).toContain("Watched");
    expect(comboboxes).toContain("Date Viewed");
  });

  it("shows rules the editor cannot edit as read-only and keeps them until removed", async () => {
    const onSave = renderAdmin(structuredClone(unknownFieldConfig));
    const readOnly = screen.getAllByRole("group", { name: "Rule not editable here" });
    expect(readOnly).toHaveLength(2);
    expect(readOnly[0]).toHaveTextContent('cast contains "Tom Hanks"');
    expect(readOnly[1]).toHaveTextContent('watched is "true"');

    await userEvent.click(within(readOnly[0]!).getByRole("button", { name: "Remove" }));
    expect(screen.getAllByRole("group", { name: "Rule not editable here" })).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave.mock.calls[0]![0].config.groups).toEqual([
      {
        match: "all",
        rules: [
          { field: "genre", op: "is", value: "Drama" },
          { field: "watched", op: "is", value: "true" },
        ],
      },
    ]);
  });
});
