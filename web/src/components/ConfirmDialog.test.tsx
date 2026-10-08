import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialog } from "./ConfirmDialog";

afterEach(cleanup);

describe("ConfirmDialog", () => {
  it("lists what the action does and says it can't be undone", async () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmDialog
        open
        onOpenChange={vi.fn()}
        title="Delete Studio Ghibli?"
        description="It goes away from every library's Collections tab."
        bullets={{ label: "Rows that go too", items: ["Ghibli on Home", "Ghibli on Movies"] }}
        irreversible
        variant="destructive"
        confirmLabel="Delete it and its 2 rows"
        onConfirm={onConfirm}
      />,
    );
    const dialog = screen.getByRole("alertdialog", { name: "Delete Studio Ghibli?" });
    const list = within(dialog).getByRole("list", { name: "Rows that go too" });
    expect(
      within(list)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["Ghibli on Home", "Ghibli on Movies"]);
    expect(within(dialog).getByText("This can't be undone.")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("reads the list and the warning as part of the dialog's description", () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={vi.fn()}
        title="Delete Studio Ghibli?"
        description="It goes away from every library's Collections tab."
        bullets={{ label: "Rows that go too", items: ["Ghibli on Home", "Ghibli on Movies"] }}
        irreversible
        variant="destructive"
        onConfirm={vi.fn()}
      />,
    );
    expect(
      screen.getByRole("alertdialog", { name: "Delete Studio Ghibli?" }),
    ).toHaveAccessibleDescription(
      "It goes away from every library's Collections tab. Ghibli on Home Ghibli on Movies This can't be undone.",
    );
  });

  it("shows neither without them", () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={vi.fn()}
        title="Sign out?"
        description="You'll need to sign in again."
        onConfirm={vi.fn()}
      />,
    );
    const dialog = screen.getByRole("alertdialog", { name: "Sign out?" });
    expect(within(dialog).queryByRole("list")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("This can't be undone.")).not.toBeInTheDocument();
    expect(dialog).toHaveAccessibleDescription("You'll need to sign in again.");
  });

  it("says why the action didn't happen, as an alert inside the dialog", () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={vi.fn()}
        title="Delete Studio Ghibli?"
        description="It's removed for everyone."
        error="Rows still use it. Remove them first."
        onConfirm={vi.fn()}
      />,
    );
    const dialog = screen.getByRole("alertdialog", { name: "Delete Studio Ghibli?" });
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Rows still use it. Remove them first.",
    );
  });
});
