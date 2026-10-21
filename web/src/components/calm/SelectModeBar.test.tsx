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
  it("refuses to act on nothing or on more than its limit", () => {
    const { rerender } = render(bar(0));
    for (const name of ["Sync lists", "Delete…"])
      expect(screen.getByRole("button", { name })).toBeDisabled();
    rerender(bar(101));
    expect(screen.getByRole("status")).toHaveTextContent("Select up to 100 collections at a time.");
    expect(screen.getByRole("button", { name: "Sync lists" })).toBeDisabled();
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
