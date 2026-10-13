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
