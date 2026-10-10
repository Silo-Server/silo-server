import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { SaveBar } from "./SaveBar";

function renderBar(props: Partial<Parameters<typeof SaveBar>[0]> = {}) {
  return render(
    <SaveBar dirtyCount={2} onSave={vi.fn()} onDiscard={vi.fn()} isSaving={false} {...props} />,
  );
}

describe("SaveBar", () => {
  it.each(["settings", "page"] as const)(
    "handles a rejected async save at the %s click boundary",
    async (placement) => {
      const onSave = vi.fn().mockRejectedValue(new Error("412 precondition failed"));
      renderBar({ onSave, placement });

      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      expect(onSave).toHaveBeenCalledExactlyOnceWith();
      expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
    },
  );

  it("stays hidden while the tab is clean", () => {
    const { container } = renderBar({ dirtyCount: 0 });

    expect(container).toBeEmptyDOMElement();
  });

  it("counts the staged changes and offers both actions", async () => {
    const onSave = vi.fn();
    const onDiscard = vi.fn();
    renderBar({ dirtyCount: 3, onSave, onDiscard });

    expect(screen.getByText("3 unsaved changes")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Discard" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onDiscard).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledTimes(1);
  });

  it("does not pass the click event to a save callback that accepts selected keys", async () => {
    const onSave = vi.fn((selectedKeys?: string[]) => {
      selectedKeys?.includes("artwork.storage_backend");
    });
    renderBar({ onSave });

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave.mock.calls).toEqual([[]]);
  });

  it("uses the singular form for one change", () => {
    renderBar({ dirtyCount: 1 });

    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
  });

  it("says nothing about restarts", () => {
    renderBar({ dirtyCount: 4 });

    expect(screen.queryByText(/restart/i)).not.toBeInTheDocument();
  });

  it("disables saving while a save is in flight", () => {
    renderBar({ isSaving: true });

    expect(screen.getByRole("button", { name: "Saving..." })).toBeDisabled();
  });

  it("keeps one status wrapper around the whole settings pill", () => {
    renderBar();

    expect(screen.queryByRole("region")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toContainElement(
      screen.getByRole("button", { name: "Save" }),
    );
  });

  it("names the staged fields in place of the count", () => {
    renderBar({ message: "Name and description not saved" });

    expect(screen.getByText("Name and description not saved")).toBeInTheDocument();
    expect(screen.queryByText("2 unsaved changes")).not.toBeInTheDocument();
  });

  it("relabels both actions and holds the primary one back until it can run", async () => {
    const onDiscard = vi.fn();
    renderBar({
      saveLabel: "Create collection",
      discardLabel: "Cancel",
      canSave: false,
      onDiscard,
    });

    expect(screen.getByRole("button", { name: "Create collection" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onDiscard).toHaveBeenCalledTimes(1);
  });
});

describe("SaveBar on a page", () => {
  it("is a labelled region that announces only its message", () => {
    renderBar({ placement: "page", message: "Name not saved" });

    const region = screen.getByRole("region", { name: "Unsaved changes" });
    const status = within(region).getByRole("status");
    expect(status).toHaveTextContent("Name not saved");
    expect(within(status).queryByRole("button")).not.toBeInTheDocument();
    expect(within(region).getByRole("button", { name: "Save" })).toBeEnabled();
    expect(within(region).getByRole("button", { name: "Discard" })).toBeEnabled();
  });

  it("shows nothing visible and no landmark while nothing is staged", () => {
    renderBar({ placement: "page", dirtyCount: 0 });

    expect(screen.queryByRole("region")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  // A live region inserted with its text already in it is often not read out.
  // The empty status stays mounted while the page is clean, so the first edit
  // fills a region the screen reader already watches.
  it("announces the first change through a status that was already mounted", () => {
    const view = renderBar({ placement: "page", dirtyCount: 0 });
    const status = screen.getByRole("status");

    view.rerender(
      <SaveBar
        dirtyCount={1}
        onSave={vi.fn()}
        onDiscard={vi.fn()}
        isSaving={false}
        placement="page"
        message="Name not saved"
      />,
    );

    expect(screen.getByRole("status")).toBe(status);
    expect(status).toHaveTextContent("Name not saved");
    expect(screen.getByRole("region", { name: "Unsaved changes" })).toContainElement(status);
  });

  // Create mode: the bar shows before anything is staged, with a muted dot,
  // its own label and the primary action held back until it can run.
  it("can show while clean with a muted dot for a collection not created yet", () => {
    renderBar({
      placement: "page",
      dirtyCount: 0,
      visible: true,
      tone: "idle",
      label: "Create collection",
      message: "Not created yet",
      saveLabel: "Create collection",
      discardLabel: "Cancel",
      canSave: false,
    });

    const region = screen.getByRole("region", { name: "Create collection" });
    const status = within(region).getByRole("status");
    expect(status).toHaveTextContent("Not created yet");
    expect(status.querySelector("[aria-hidden='true']")).toHaveClass("bg-muted-foreground");
    expect(status.querySelector("[aria-hidden='true']")).not.toHaveClass("bg-warning");
    expect(within(region).getByRole("button", { name: "Create collection" })).toBeDisabled();
    expect(within(region).getByRole("button", { name: "Cancel" })).toBeEnabled();
  });

  it("can stay hidden while edits are staged, e.g. while the page loads", () => {
    renderBar({ placement: "page", visible: false });

    expect(screen.queryByRole("region")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
