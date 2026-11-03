import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createContext, useContext, type ReactNode } from "react";

const mocks = vi.hoisted(() => ({
  useItemFiles: vi.fn(),
  searchMutate: vi.fn(),
}));

vi.mock("@/hooks/queries/items", () => ({
  useItemFiles: (...args: unknown[]) => mocks.useItemFiles(...args),
  useSearchItemMatchCandidates: () => ({
    mutate: mocks.searchMutate,
    isPending: false,
    isSuccess: false,
    data: undefined,
  }),
  useSplitItem: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 2, name: "TV Shows" },
      { id: 5, name: "Sports" },
    ],
  }),
}));

vi.mock("@/components/ui/dialog", () => ({
  Dialog: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div data-testid="dialog">{children}</div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
}));

// Radix Select needs layout APIs jsdom lacks; render each option as a button.
const SelectChange = createContext<(value: string) => void>(() => {});
vi.mock("@/components/ui/select", () => ({
  Select: ({
    children,
    onValueChange,
  }: {
    children: ReactNode;
    onValueChange: (value: string) => void;
  }) => <SelectChange.Provider value={onValueChange}>{children}</SelectChange.Provider>,
  SelectContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectItem: ({ children, value }: { children: ReactNode; value: string }) => {
    const onValueChange = useContext(SelectChange);
    return (
      <button type="button" onClick={() => onValueChange(value)}>
        {children}
      </button>
    );
  },
  SelectTrigger: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectValue: () => null,
}));

import SplitItemDialog from "./SplitItemDialog";

const series = {
  content_id: "series-tvdb-359913",
  title: "Formula 1: Drive to Survive",
  year: 2019,
  type: "series",
};

function file(id: string, libraryId: string) {
  return {
    id,
    library_id: libraryId,
    file_path: `/media/lib${libraryId}/F1/S01E0${id}.mkv`,
    observed_root_path: `/media/lib${libraryId}/F1`,
  };
}

function filesIn(...libraryIds: string[]) {
  return {
    data: { files: libraryIds.map((libraryId, index) => file(String(index + 1), libraryId)) },
    isLoading: false,
  };
}

const searchButton = () => screen.getByRole("button", { name: "Search" });
const searchedLibrary = () =>
  (mocks.searchMutate.mock.lastCall?.[0] as { library_id?: number }).library_id;

describe("SplitItemDialog search library", () => {
  beforeEach(() => {
    mocks.searchMutate.mockReset();
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("searches in the library the selected files come from", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "2", "5", "5"));
    render(<SplitItemDialog item={series} open onOpenChange={() => {}} />);

    fireEvent.click(screen.getByLabelText("Select all files in /media/lib5/F1"));
    expect(screen.queryByRole("button", { name: "Sports" })).not.toBeInTheDocument();
    fireEvent.click(searchButton());
    expect(searchedLibrary()).toBe(5);
  });

  it("falls back to the page's library scope when the selection spans libraries", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "5", "5"));
    render(<SplitItemDialog item={{ ...series, library_id: 2 }} open onOpenChange={() => {}} />);

    fireEvent.click(screen.getByLabelText("Select /media/lib2/F1/S01E01.mkv"));
    fireEvent.click(screen.getByLabelText("Select /media/lib5/F1/S01E02.mkv"));
    expect(screen.queryByRole("button", { name: "Sports" })).not.toBeInTheDocument();
    fireEvent.click(searchButton());
    expect(searchedLibrary()).toBe(2);
  });

  it("asks for a library when neither the selection nor the page names one", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "5", "5"));
    render(<SplitItemDialog item={series} open onOpenChange={() => {}} />);

    expect(searchButton()).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Sports" }));
    fireEvent.click(searchButton());
    expect(searchedLibrary()).toBe(5);

    // Once the selection settles on one library, that library wins.
    fireEvent.click(screen.getByLabelText("Select all files in /media/lib2/F1"));
    expect(screen.queryByRole("button", { name: "Sports" })).not.toBeInTheDocument();
    fireEvent.click(searchButton());
    expect(searchedLibrary()).toBe(2);
  });

  it("searches a single-library item unscoped, as before", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "2", "2"));
    render(<SplitItemDialog item={series} open onOpenChange={() => {}} />);

    fireEvent.click(screen.getByLabelText("Select /media/lib2/F1/S01E01.mkv"));
    expect(screen.queryByRole("button", { name: "TV Shows" })).not.toBeInTheDocument();
    fireEvent.click(searchButton());
    expect(searchedLibrary()).toBeUndefined();
  });
});
