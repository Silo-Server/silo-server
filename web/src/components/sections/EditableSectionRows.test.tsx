import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { RecipeCatalogResponse } from "@/lib/recipes";

import { recipeLabel, SortableSectionTableRow } from "./EditableSectionRows";

vi.mock("@dnd-kit/sortable", () => ({
  useSortable: () => ({
    attributes: {},
    listeners: {},
    setNodeRef: vi.fn(),
    transform: null,
    transition: undefined,
    isDragging: false,
  }),
}));

function renderRow(onSelectionChange: (checked: boolean, extendRange: boolean) => void) {
  return render(
    <table>
      <tbody>
        <SortableSectionTableRow
          section={{
            id: "section-two",
            title: "Section Two",
            sectionType: "recently_added",
            itemLimit: 12,
            featured: false,
            enabled: true,
          }}
          canReorder={false}
          libraries={[]}
          collectionLabels={new Map()}
          selected={false}
          selectionLabel="Select Section Two home section"
          onSelectionChange={onSelectionChange}
          onEdit={vi.fn()}
          onDelete={vi.fn()}
        />
      </tbody>
    </table>,
  );
}

describe("SortableSectionTableRow selection", () => {
  it("selects only its checkbox on a normal click", () => {
    const onSelectionChange = vi.fn();
    renderRow(onSelectionChange);

    fireEvent.click(screen.getByRole("checkbox", { name: "Select Section Two home section" }));

    expect(onSelectionChange).toHaveBeenCalledWith(true, false);
  });

  it("requests a range selection on a shift-click", () => {
    const onSelectionChange = vi.fn();
    renderRow(onSelectionChange);

    fireEvent.click(screen.getByRole("checkbox", { name: "Select Section Two home section" }), {
      shiftKey: true,
    });

    expect(onSelectionChange).toHaveBeenCalledWith(true, true);
  });
});

describe("recipeLabel", () => {
  const catalog: RecipeCatalogResponse = {
    categories: {
      social: [
        {
          type: "trending_discover",
          category: "social",
          avoid_duplicates: false,
          supports_rotation: false,
          admin_only: false,
          presets: [
            {
              key: "tdisc_tmdb_day",
              display_name: "TMDB Trending Today",
              icon: "",
              description_short: "",
              default_params: { source: "tmdb", window: "day" },
            },
            {
              key: "tdisc_tmdb_week",
              display_name: "TMDB Trending This Week",
              icon: "",
              description_short: "",
              default_params: { source: "tmdb", window: "week" },
            },
          ],
        },
      ],
    },
  };

  it("names the preset that matches the section config", () => {
    expect(recipeLabel(catalog, "trending_discover", { source: "tmdb", window: "week" })).toBe(
      "TMDB Trending This Week",
    );
    expect(recipeLabel(catalog, "trending_discover", { source: "tmdb", window: "day" })).toBe(
      "TMDB Trending Today",
    );
  });

  it("falls back to the first preset when no preset matches", () => {
    expect(recipeLabel(catalog, "trending_discover")).toBe("TMDB Trending Today");
  });
});
