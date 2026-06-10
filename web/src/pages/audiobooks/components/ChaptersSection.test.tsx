import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChaptersSection } from "./ChaptersSection";
import type { AudiobookFile } from "@/lib/audiobooks/types";

const files: AudiobookFile[] = [
  {
    id: 1,
    path: "a",
    duration_seconds: 600,
    chapters: [
      { index: 0, title: "Prologue", source: "embedded", start_seconds: 0, end_seconds: 200 },
      { index: 1, title: "Memory", source: "embedded", start_seconds: 200, end_seconds: 600 },
    ],
  },
];

async function expandChapters(): Promise<void> {
  await userEvent.click(screen.getByRole("button", { name: /^chapters/i }));
}

describe("ChaptersSection", () => {
  it("renders chapter section collapsed by default", () => {
    render(<ChaptersSection files={files} currentPositionSeconds={null} onSelect={vi.fn()} />);
    expect(screen.queryByRole("button", { name: /Prologue/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Memory/ })).not.toBeInTheDocument();
    // Header toggle is visible.
    expect(screen.getByRole("button", { name: /^chapters/i })).toBeInTheDocument();
  });

  it("expands when the header is clicked", async () => {
    render(<ChaptersSection files={files} currentPositionSeconds={null} onSelect={vi.fn()} />);
    await expandChapters();
    expect(screen.getByRole("button", { name: /Prologue/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Memory/ })).toBeInTheDocument();
  });

  it("highlights the currently-listening chapter once expanded", async () => {
    render(<ChaptersSection files={files} currentPositionSeconds={250} onSelect={vi.fn()} />);
    await expandChapters();
    const row = screen.getByRole("button", { name: /Memory/ });
    expect(row).toHaveAttribute("data-current", "true");
    expect(within(row).getByText("listening")).toBeInTheDocument();
  });

  it("sort menu switches between position and longest-first orders", async () => {
    render(<ChaptersSection files={files} currentPositionSeconds={null} onSelect={vi.fn()} />);
    await expandChapters();
    const rowsBefore = screen.getAllByRole("button", { name: /Prologue|Memory/ });
    expect(within(rowsBefore[0]!).getByText("Prologue")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /sort/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: /longest first/i }));

    const rowsAfter = screen.getAllByRole("button", { name: /Prologue|Memory/ });
    expect(within(rowsAfter[0]!).getByText("Memory")).toBeInTheDocument();
  });

  it("calls onSelect with absolute start seconds when a chapter is clicked", async () => {
    const onSelect = vi.fn();
    render(<ChaptersSection files={files} currentPositionSeconds={null} onSelect={onSelect} />);
    await expandChapters();
    await userEvent.click(screen.getByRole("button", { name: /Memory/ }));
    expect(onSelect).toHaveBeenCalledWith(200);
  });
});
