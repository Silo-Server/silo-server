import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const mocks = vi.hoisted(() => ({
  useSearchItemMatchCandidates: vi.fn(),
  useApplyItemMatch: vi.fn(),
  useCatalogItemDetail: vi.fn(),
}));

vi.mock("@/hooks/queries/items", () => ({
  useSearchItemMatchCandidates: (...args: unknown[]) => mocks.useSearchItemMatchCandidates(...args),
  useApplyItemMatch: (...args: unknown[]) => mocks.useApplyItemMatch(...args),
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
  });

  it("renders title, year, IMDb, TMDB, and TVDB inputs", () => {
    const markup = renderToStaticMarkup(
      <MatchItemDialog item={baseItem} open={true} onOpenChange={() => {}} />,
    );

    expect(markup).toContain('id="match-title"');
    expect(markup).toContain('id="match-year"');
    expect(markup).toContain('id="match-imdb"');
    expect(markup).toContain('id="match-tmdb"');
    expect(markup).toContain('id="match-tvdb"');
  });

  it("renders a search button", () => {
    const markup = renderToStaticMarkup(
      <MatchItemDialog item={baseItem} open={true} onOpenChange={() => {}} />,
    );

    expect(markup).toContain("Search");
  });

  it("renders candidate list when search returns results", () => {
    mocks.useSearchItemMatchCandidates.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      isSuccess: true,
      data: {
        candidates: [
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
        ],
      },
    });

    const markup = renderToStaticMarkup(
      <MatchItemDialog item={baseItem} open={true} onOpenChange={() => {}} />,
    );

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
    mocks.useSearchItemMatchCandidates.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      isSuccess: true,
      data: {
        candidates: [
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
        ],
      },
    });

    const markup = renderToStaticMarkup(
      <MatchItemDialog item={baseItem} open={true} onOpenChange={() => {}} />,
    );

    expect(markup).toContain(">10 Tokyo Warriors</");
    expect(markup).toContain("Native title: 倒凶十将伝");
    expect(markup).not.toContain("Matched alias:");
  });

  it("keeps a confirmed localized title ahead of a native matched alias", () => {
    mocks.useSearchItemMatchCandidates.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      isSuccess: true,
      data: {
        candidates: [
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
        ],
      },
    });

    const markup = renderToStaticMarkup(
      <MatchItemDialog item={baseItem} open={true} onOpenChange={() => {}} />,
    );

    expect(markup).toContain(">10 Tokyo Warriors</");
    expect(markup).toContain("Original: 倒凶十将伝");
    // The matched alias equals the original title shown directly above it;
    // repeating the same string as a second line is noise, so it is suppressed.
    expect(markup).not.toContain("Matched alias: 倒凶十将伝");
    expect(markup).not.toContain("Native title: 10 Tokyo Warriors");
  });

  it("shows no results message when search returns empty", () => {
    mocks.useSearchItemMatchCandidates.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      isSuccess: true,
      data: { candidates: [] },
    });

    const markup = renderToStaticMarkup(
      <MatchItemDialog item={baseItem} open={true} onOpenChange={() => {}} />,
    );

    expect(markup).toContain("No candidates found");
  });

  it("displays current item summary with title, year, and type badge", () => {
    const markup = renderToStaticMarkup(
      <MatchItemDialog item={baseItem} open={true} onOpenChange={() => {}} />,
    );

    expect(markup).toContain("Inception");
    expect(markup).toContain("(2010)");
    expect(markup).toContain("movie");
  });

  it("renders local media rows when file paths are available", () => {
    const markup = renderToStaticMarkup(
      <MatchItemDialog
        item={{
          ...baseItem,
          versions: [
            {
              file_id: 7,
              file_path: "/media/Movies/Inception (2010)/Inception.4K.mkv",
              file_name: "Inception.4K.mkv",
              resolution: "2160p",
              codec_video: "hevc",
              codec_audio: "dts",
              hdr: true,
              container: "mkv",
              file_size: 1,
              duration: 1,
              bitrate: 1,
            },
          ],
        }}
        open={true}
        onOpenChange={() => {}}
      />,
    );

    expect(markup).toContain("Local media");
    expect(markup).toContain("Inception (2010)/");
    expect(markup).toContain("Inception.4K.mkv");
    expect(markup).toContain("2160p");
  });
});
