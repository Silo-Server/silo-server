import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RefreshCw, Trash2 } from "lucide-react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SelectAllHeader, SelectModeBar } from "./SelectModeBar";

afterEach(cleanup);

function bar(count: number, sync = vi.fn()) {
  return (
    <SelectModeBar
      count={count}
      limit={100}
      noun="collections"
      actions={[
        { key: "sync", label: "Sync lists", icon: RefreshCw, onClick: sync },
        {
          key: "delete",
          label: "Delete…",
          icon: Trash2,
          onClick: vi.fn(),
          destructive: true,
          separated: true,
        },
      ]}
    />
  );
}

describe("SelectModeBar", () => {
  it("names what is selected and runs an action", async () => {
    const sync = vi.fn();
    render(bar(3, sync));
    expect(screen.getByRole("group", { name: "Selected collections" })).toHaveTextContent(
      "3 selected",
    );
    await userEvent.click(screen.getByRole("button", { name: "Sync lists" }));
    expect(sync).toHaveBeenCalledTimes(1);
  });

  it("refuses to act on nothing or on more than its limit", () => {
    const { rerender } = render(bar(0));
    for (const name of ["Sync lists", "Delete…"])
      expect(screen.getByRole("button", { name })).toBeDisabled();
    rerender(bar(101));
    expect(screen.getByRole("status")).toHaveTextContent("Select up to 100 collections at a time.");
    expect(screen.getByRole("button", { name: "Sync lists" })).toBeDisabled();
  });

  it("turns off one action that doesn't apply, and says why under the bar", () => {
    render(
      <SelectModeBar
        count={2}
        limit={100}
        noun="collections"
        note="Sync skips smart collections (2 here)."
        actions={[
          {
            key: "sync",
            label: "Sync 0 lists",
            icon: RefreshCw,
            onClick: vi.fn(),
            disabled: true,
            explainedByNote: true,
          },
          { key: "delete", label: "Delete…", icon: Trash2, onClick: vi.fn() },
        ]}
      />,
    );
    const sync = screen.getByRole("button", { name: "Sync 0 lists" });
    expect(sync).toBeDisabled();
    // A screen reader on the off button hears why.
    expect(sync).toHaveAccessibleDescription("Sync skips smart collections (2 here).");
    const remove = screen.getByRole("button", { name: "Delete…" });
    expect(remove).toBeEnabled();
    expect(remove).not.toHaveAttribute("aria-describedby");
  });
});

describe("SelectAllHeader", () => {
  it("is mixed while some are selected and selects all when ticked", async () => {
    const onSelectAll = vi.fn();
    render(
      <SelectAllHeader
        count={4}
        selectedCount={2}
        limit={100}
        noun="collections"
        onSelectAll={onSelectAll}
        onDone={vi.fn()}
      />,
    );
    expect(screen.getByText("Up to 100 collections at a time")).toBeInTheDocument();
    const all = screen.getByRole("checkbox", { name: "Select all" });
    expect(all).toHaveAttribute("aria-checked", "mixed");
    await userEvent.click(all);
    expect(onSelectAll).toHaveBeenCalledWith(true, false);
  });
});
