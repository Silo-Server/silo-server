import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { LibraryCollectionGroup } from "@/api/types";

import { GroupEditDialog } from "./GroupEditDialog";

const group = {
  id: "group-1",
  name: "Franchises",
  kind: "regular",
  default_sort_mode: "manual",
} as LibraryCollectionGroup;

function renderDialog(onDelete = vi.fn()) {
  render(
    <GroupEditDialog
      group={group}
      mode="edit"
      onSubmit={vi.fn()}
      onDelete={onDelete}
      onCancel={vi.fn()}
    />,
  );
  return onDelete;
}

describe("GroupEditDialog delete", () => {
  it("asks before deleting and says the collections are kept", () => {
    const onDelete = renderDialog();

    fireEvent.click(screen.getByRole("button", { name: "Delete group" }));

    expect(onDelete).not.toHaveBeenCalled();
    const confirm = screen.getByRole("alertdialog");
    expect(confirm).toHaveTextContent('Delete group "Franchises"?');
    expect(confirm).toHaveTextContent("move to Ungrouped");
    expect(confirm).toHaveTextContent("No collections are deleted");

    fireEvent.click(within(confirm).getByRole("button", { name: "Delete group" }));
    expect(onDelete).toHaveBeenCalledOnce();
  });

  it("keeps the group when the confirmation is cancelled", () => {
    const onDelete = renderDialog();

    fireEvent.click(screen.getByRole("button", { name: "Delete group" }));
    fireEvent.click(
      within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel" }),
    );

    expect(onDelete).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });
});
