import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LibraryCollection } from "@/api/types";

import { CollectionRow } from "./CollectionRow";

const selection = vi.hoisted(() => ({
  selectOnly: vi.fn(),
  toggleOne: vi.fn(),
  selectRange: vi.fn(),
}));

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

vi.mock("./GroupsBoard", () => ({
  useSelection: () => ({
    isSelected: () => false,
    selectOnly: selection.selectOnly,
    toggleOne: selection.toggleOne,
    selectRange: selection.selectRange,
  }),
}));

const collection = {
  id: "collection-two",
  title: "Collection Two",
  collection_type: "manual",
  item_count: 2,
} as LibraryCollection;

function renderRow() {
  return render(
    <CollectionRow
      collection={collection}
      parentGroupID="group-one"
      parentCollectionIDs={["collection-one", "collection-two", "collection-three"]}
      onEdit={vi.fn()}
      onDelete={vi.fn()}
      onSync={vi.fn()}
    />,
  );
}

describe("CollectionRow selection", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("toggles only its checkbox on a normal click", () => {
    renderRow();

    fireEvent.click(screen.getByRole("checkbox", { name: "Select Collection Two" }));

    expect(selection.toggleOne).toHaveBeenCalledWith("collection-two", "collection", "group-one");
    expect(selection.selectOnly).not.toHaveBeenCalled();
    expect(selection.selectRange).not.toHaveBeenCalled();
  });

  it("selects through the anchor on a shift-click", () => {
    renderRow();

    fireEvent.click(screen.getByRole("checkbox", { name: "Select Collection Two" }), {
      shiftKey: true,
    });

    expect(selection.selectRange).toHaveBeenCalledWith(
      "collection-two",
      "collection",
      "group-one",
      ["collection-one", "collection-two", "collection-three"],
      true,
    );
    expect(selection.selectOnly).not.toHaveBeenCalled();
    expect(selection.toggleOne).not.toHaveBeenCalled();
  });
});
