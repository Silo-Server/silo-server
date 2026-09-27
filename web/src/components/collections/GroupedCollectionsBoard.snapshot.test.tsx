import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { GroupedCollectionsBoard } from "./GroupedCollectionsBoard";

describe("group editor snapshots", () => {
  it("keeps the callback captured before editing when the list refreshes", async () => {
    const originalCommit = vi.fn();
    const refreshedCommit = vi.fn();
    const props = {
      items: [{ id: "collection", group_id: "g" }],
      groups: [{ id: "g", name: "Original", default_sort_mode: "manual" as const, sort_order: 0 }],
      renderItem: () => <div>Collection</div>,
    };
    const { rerender } = render(
      <GroupedCollectionsBoard
        {...props}
        onPrepareRenameGroup={async () => ({ title: "Observed name", commit: originalCommit })}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Rename group" }));
    const input = await screen.findByDisplayValue("Observed name");
    rerender(
      <GroupedCollectionsBoard
        {...props}
        groups={[{ ...props.groups[0]!, name: "Background change" }]}
        onPrepareRenameGroup={async () => ({ title: "Background change", commit: refreshedCommit })}
      />,
    );
    fireEvent.change(input, { target: { value: "My draft" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(originalCommit).toHaveBeenCalledWith("My draft"));
    expect(refreshedCommit).not.toHaveBeenCalled();
  });
});
