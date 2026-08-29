import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { SaveBar } from "./SaveBar";

function renderBar(props: Partial<Parameters<typeof SaveBar>[0]> = {}) {
  return render(
    <SaveBar dirtyCount={2} onSave={vi.fn()} onDiscard={vi.fn()} isSaving={false} {...props} />,
  );
}

describe("SaveBar", () => {
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

  // The restart prompt belongs to the admin shell (see
  // components/admin/RestartBanner.test.tsx); the pill must never grow one.
  it("renders no restart prompt of its own", () => {
    renderBar({ dirtyCount: 2 });

    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();
  });
});
