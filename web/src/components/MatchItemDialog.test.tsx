import { fireEvent, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createContext, useContext, type ReactNode } from "react";

const mocks = vi.hoisted(() => ({
  useSearchItemMatchCandidates: vi.fn(),
  useApplyItemMatch: vi.fn(),
  useCatalogItemDetail: vi.fn(),
  useItemFiles: vi.fn(),
  useIsActingAdmin: vi.fn(),
}));

vi.mock("@/hooks/queries/items", () => ({
  useSearchItemMatchCandidates: (...args: unknown[]) => mocks.useSearchItemMatchCandidates(...args),
  useApplyItemMatch: (...args: unknown[]) => mocks.useApplyItemMatch(...args),
  useItemFiles: (...args: unknown[]) => mocks.useItemFiles(...args),
}));

vi.mock("@/hooks/useIsActingAdmin", () => ({
  useIsActingAdmin: () => mocks.useIsActingAdmin(),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 2, name: "TV Shows" },
      { id: 5, name: "Sports" },
    ],
  }),
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

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (...args: unknown[]) => mocks.useCatalogItemDetail(...args),
}));

// Mock Dialog components to render inline (Radix portals don't render in SSR)
vi.mock("@/components/ui/dialog", () => ({
  Dialog: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div data-testid="dialog">{children}</div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
}));

import MatchItemDialog from "./MatchItemDialog";

const baseItem = {
  content_id: "movie-1",
  title: "Inception",
  year: 2010,
  type: "movie" as const,
};

function searchReturns(candidates: unknown[]) {
  mocks.useSearchItemMatchCandidates.mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
    isSuccess: true,
    data: { candidates },
  });
}

function dialogMarkup() {
  return renderToStaticMarkup(
    <MatchItemDialog item={baseItem} open={true} onOpenChange={() => {}} />,
  );
}

describe("MatchItemDialog", () => {
  beforeEach(() => {
    const mutate = vi.fn();

    mocks.useSearchItemMatchCandidates.mockReturnValue({
      mutate,
      isPending: false,
      isSuccess: false,
      data: null,
    });
    mocks.useApplyItemMatch.mockReturnValue({
      mutate,
      isPending: false,
    });
    mocks.useCatalogItemDetail.mockReturnValue({
      data: undefined,
      isLoading: false,
    });
    mocks.useItemFiles.mockReturnValue({ data: undefined, isLoading: false });
    mocks.useIsActingAdmin.mockReturnValue(true);
  });

  it("renders candidate list when search returns results", () => {
    searchReturns([
      {
        title: "Inception",
        year: 2010,
        content_type: "movie",
        overview: "A thief who steals secrets...",
        image_url: "https://image.tmdb.org/poster.jpg",
        provider_ids: { tmdb: "27205", imdb: "tt1375666" },
        sources: ["tmdb", "tvdb"],
        agreement_hints: ["agreed_by_tmdb_and_tvdb"],
        match_score: 87.25,
        match_reasons: ["title_exact", "provider_id_consensus"],
      },
      {
        title: "Inception (TV)",
        year: 2012,
        content_type: "series",
        overview: "An unrelated series",
        image_url: "",
        provider_ids: { tvdb: "999" },
        sources: ["tvdb"],
        agreement_hints: [],
      },
    ]);

    const markup = dialogMarkup();

    expect(markup).toContain("Inception");
    expect(markup).toContain("Inception (TV)");
    expect(markup).toContain("tmdb");
    expect(markup).toContain("2 sources agree");
    expect(markup).toContain("Score 87.3");
    expect(markup).toContain("Match reasons:");
    expect(markup).toContain("title exact, provider id consensus");
    expect(markup).toContain('data-testid="match-candidate"');
  });

  it("shows a matched library-language alias before a native fallback title", () => {
    searchReturns([
      {
        title: "倒凶十将伝",
        original_title: "倒凶十将伝",
        title_language: "ja",
        title_is_fallback: true,
        matched_title: "10 Tokyo Warriors",
        year: 1999,
        content_type: "series",
        image_url: "",
        overview: "",
        provider_ids: { tvdb: "123" },
        sources: ["tvdb"],
        agreement_hints: [],
      },
    ]);

    const markup = dialogMarkup();

    expect(markup).toContain(">10 Tokyo Warriors</");
    expect(markup).toContain("Native title: 倒凶十将伝");
    expect(markup).not.toContain("Matched alias:");
  });

  it("keeps a confirmed localized title ahead of a native matched alias", () => {
    searchReturns([
      {
        title: "10 Tokyo Warriors",
        original_title: "倒凶十将伝",
        title_language: "en",
        matched_title: "倒凶十将伝",
        year: 1999,
        content_type: "series",
        image_url: "",
        overview: "",
        provider_ids: { tvdb: "123" },
        sources: ["tvdb"],
        agreement_hints: [],
      },
    ]);

    const markup = dialogMarkup();

    expect(markup).toContain(">10 Tokyo Warriors</");
    expect(markup).toContain("Original: 倒凶十将伝");
    // The matched alias equals the original title shown directly above it;
    // repeating the same string as a second line is noise, so it is suppressed.
    expect(markup).not.toContain("Matched alias: 倒凶十将伝");
    expect(markup).not.toContain("Native title: 10 Tokyo Warriors");
  });
});

describe("MatchItemDialog apply", () => {
  const item = { content_id: "local-abc", title: "The Office", year: 0, type: "series" as const };
  const applyMutate = vi.fn();

  beforeEach(() => {
    applyMutate.mockReset();
    searchReturns([
      {
        title: "The Office",
        year: 2001,
        content_type: "series",
        overview: "",
        image_url: "",
        provider_ids: { tvdb: "78107" },
        sources: ["tvdb"],
        agreement_hints: [],
      },
    ]);
    mocks.useApplyItemMatch.mockReturnValue({ mutate: applyMutate, isPending: false });
    mocks.useItemFiles.mockReturnValue({ data: undefined, isLoading: false });
    mocks.useIsActingAdmin.mockReturnValue(true);
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("hands onReplaced to the apply mutation and closes on success", () => {
    const onOpenChange = vi.fn();
    const onReplaced = vi.fn();
    render(
      <MatchItemDialog item={item} open onOpenChange={onOpenChange} onReplaced={onReplaced} />,
    );
    fireEvent.click(screen.getByTestId("match-candidate"));
    fireEvent.click(screen.getByRole("button", { name: "Apply Match" }));

    const [variables, options] = applyMutate.mock.calls[0] as [
      {
        item: typeof item;
        providerIds: Record<string, string>;
        onReplaced: (contentID: string) => void;
      },
      { onSuccess: () => void },
    ];
    expect(variables).toMatchObject({ item, providerIds: { tvdb: "78107" } });
    options.onSuccess();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  // The page keys the dialog by content ID, so following the new ID unmounts
  // it before the per-call onSuccess runs; the dialog has to close first.
  it("closes before following a moved item", () => {
    const calls: string[] = [];
    const onOpenChange = vi.fn((open: boolean) => calls.push(`open:${open}`));
    const onReplaced = vi.fn((contentID: string) => {
      calls.push(`replaced:${contentID}`);
    });
    render(
      <MatchItemDialog item={item} open onOpenChange={onOpenChange} onReplaced={onReplaced} />,
    );
    fireEvent.click(screen.getByTestId("match-candidate"));
    fireEvent.click(screen.getByRole("button", { name: "Apply Match" }));

    const [variables] = applyMutate.mock.calls[0] as [{ onReplaced: (contentID: string) => void }];
    variables.onReplaced("series-tvdb-78107");
    expect(calls).toEqual(["open:false", "replaced:series-tvdb-78107"]);
  });
});

describe("MatchItemDialog library scope", () => {
  const series = {
    content_id: "series-tvdb-359913",
    title: "Formula 1: Drive to Survive",
    year: 2019,
    type: "series" as const,
  };
  const searchMutate = vi.fn();
  const applyMutate = vi.fn();
  const filesIn = (...libraryIds: string[]) => ({
    data: {
      files: libraryIds.map((library_id, index) => ({
        id: String(index + 1),
        library_id,
        file_path: `/media/${library_id}/S01E0${index + 1}.mkv`,
        observed_root_path: `/media/${library_id}`,
      })),
    },
    isLoading: false,
  });

  beforeEach(() => {
    searchMutate.mockReset();
    applyMutate.mockReset();
    mocks.useSearchItemMatchCandidates.mockReturnValue({
      mutate: searchMutate,
      reset: vi.fn(),
      isPending: false,
      isSuccess: true,
      data: {
        candidates: [
          {
            title: "Formula 1: Drive to Survive",
            year: 2019,
            content_type: "series",
            overview: "",
            image_url: "",
            provider_ids: { tvdb: "359913" },
            sources: ["tvdb"],
            agreement_hints: [],
          },
        ],
      },
    });
    mocks.useApplyItemMatch.mockReturnValue({ mutate: applyMutate, isPending: false });
    mocks.useCatalogItemDetail.mockReturnValue({ data: undefined, isLoading: false });
    mocks.useIsActingAdmin.mockReturnValue(true);
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("asks which of the item's libraries to search when none is in scope", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "5", "5"));
    render(<MatchItemDialog item={series} open onOpenChange={() => {}} />);

    expect(mocks.useItemFiles).toHaveBeenCalledWith(series.content_id);
    expect(screen.getByRole("button", { name: "Search" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "TV Shows" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Sports" }));
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(searchMutate).toHaveBeenCalledWith(expect.objectContaining({ library_id: 5 }));

    fireEvent.click(screen.getByTestId("match-candidate"));
    fireEvent.click(screen.getByRole("button", { name: "Apply Match" }));
    const [variables] = applyMutate.mock.calls[0] as [{ item: { library_id?: number } }];
    expect(variables.item.library_id).toBe(5);
  });

  it("asks again when the picked library is no longer one of the item's", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "5", "7"));
    const { rerender } = render(<MatchItemDialog item={series} open onOpenChange={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Sports" }));
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(screen.getByTestId("match-candidate"));

    mocks.useItemFiles.mockReturnValue(filesIn("2", "7"));
    rerender(<MatchItemDialog item={series} open onOpenChange={() => {}} />);

    expect(screen.getByRole("button", { name: "Search" })).toBeDisabled();
    // A candidate picked for the old library must not apply unscoped.
    expect(screen.queryByRole("button", { name: "Apply Match" })).not.toBeInTheDocument();
  });

  it("drops a candidate searched in a library the item has left", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "5"));
    const { rerender } = render(<MatchItemDialog item={series} open onOpenChange={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Sports" }));
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(screen.getByTestId("match-candidate"));

    // Now only in library 2: an unscoped apply would land there with a
    // candidate found under library 5's settings.
    mocks.useItemFiles.mockReturnValue(filesIn("2"));
    rerender(<MatchItemDialog item={series} open onOpenChange={() => {}} />);
    expect(screen.queryByTestId("match-candidate")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply Match" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(searchMutate).toHaveBeenLastCalledWith(
      expect.objectContaining({ library_id: undefined }),
    );
    fireEvent.click(screen.getByTestId("match-candidate"));
    fireEvent.click(screen.getByRole("button", { name: "Apply Match" }));
    expect(applyMutate).toHaveBeenCalledTimes(1);
  });

  it("uses the caller's library scope without asking", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "5"));
    render(<MatchItemDialog item={{ ...series, library_id: 2 }} open onOpenChange={() => {}} />);

    expect(mocks.useItemFiles).toHaveBeenCalledWith(undefined);
    expect(screen.queryByRole("button", { name: "Sports" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(searchMutate).toHaveBeenCalledWith(expect.objectContaining({ library_id: 2 }));

    fireEvent.click(screen.getByTestId("match-candidate"));
    fireEvent.click(screen.getByRole("button", { name: "Apply Match" }));
    const [variables] = applyMutate.mock.calls[0] as [{ item: { library_id?: number } }];
    expect(variables.item.library_id).toBe(2);
  });

  it("searches a single-library item unscoped, as before", () => {
    mocks.useItemFiles.mockReturnValue(filesIn("2", "2"));
    render(<MatchItemDialog item={series} open onOpenChange={() => {}} />);

    expect(screen.queryByRole("button", { name: "TV Shows" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(searchMutate).toHaveBeenCalledWith(expect.objectContaining({ library_id: undefined }));
  });

  it("waits for the library lookup before searching", () => {
    mocks.useItemFiles.mockReturnValue({ data: undefined, isLoading: true });
    render(<MatchItemDialog item={series} open onOpenChange={() => {}} />);

    expect(screen.getByRole("button", { name: "Search" })).toBeDisabled();
  });

  it("skips the admin-only library lookup for other curators", () => {
    mocks.useIsActingAdmin.mockReturnValue(false);
    mocks.useItemFiles.mockReturnValue({ data: undefined, isLoading: false });
    render(<MatchItemDialog item={series} open onOpenChange={() => {}} />);

    expect(mocks.useItemFiles).toHaveBeenCalledWith(undefined);
    expect(screen.getByRole("button", { name: "Search" })).toBeEnabled();
  });
});
