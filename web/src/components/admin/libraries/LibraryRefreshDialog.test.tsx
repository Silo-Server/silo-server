import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { LibraryRefreshDialog } from "./LibraryRefreshDialog";

describe("LibraryRefreshDialog", () => {
  it("is closed until a library is chosen", () => {
    render(<LibraryRefreshDialog libraryName={null} onOpenChange={vi.fn()} onConfirm={vi.fn()} />);

    expect(screen.queryByText("Refresh Library Metadata")).toBeNull();
  });

  it("confirms a quick refresh of missing metadata", async () => {
    const onConfirm = vi.fn();
    render(
      <LibraryRefreshDialog libraryName="TV Shows" onOpenChange={vi.fn()} onConfirm={onConfirm} />,
    );

    expect(screen.getByText(/Choose how much of TV Shows to refresh/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Refresh Missing Metadata/ }));

    expect(onConfirm).toHaveBeenCalledWith("quick");
  });

  it("confirms a full refresh of every item", async () => {
    const onConfirm = vi.fn();
    render(
      <LibraryRefreshDialog libraryName="TV Shows" onOpenChange={vi.fn()} onConfirm={onConfirm} />,
    );

    await userEvent.click(screen.getByRole("button", { name: /Refresh All Metadata/ }));

    expect(onConfirm).toHaveBeenCalledWith("full");
  });

  it("disables both choices while a refresh is being queued", () => {
    render(
      <LibraryRefreshDialog
        libraryName="TV Shows"
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
        isPending
      />,
    );

    expect(screen.getByRole("button", { name: /Refresh Missing Metadata/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Refresh All Metadata/ })).toBeDisabled();
  });

  it("closes from Cancel", async () => {
    const onOpenChange = vi.fn();
    render(
      <LibraryRefreshDialog
        libraryName="TV Shows"
        onOpenChange={onOpenChange}
        onConfirm={vi.fn()}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
