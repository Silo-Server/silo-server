// @vitest-environment jsdom

import { act, useEffect, useImperativeHandle, forwardRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FileVersion, ItemDetail } from "@/api/types";
import EbookReader from "./EbookReader";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
  readerPrev: vi.fn(),
  readerNext: vi.fn(),
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: mocks.useCatalogItemDetail,
}));

vi.mock("@/components/PageBack", () => ({
  default: () => <div />,
}));

vi.mock("@/reader/FoliateBookReader", async () => {
  const actual = await vi.importActual<typeof import("@/reader/FoliateBookReader")>(
    "@/reader/FoliateBookReader",
  );

  return {
    ...actual,
    default: forwardRef<
      { prev: () => void; next: () => void },
      {
        file: FileVersion;
        onProgressChange?: (progress: number | null) => void;
        onFileLoaded?: (state: { objectUrl: string; filename: string } | null) => void;
      }
    >(function MockFoliateBookReader({ file, onProgressChange, onFileLoaded }, ref) {
      useImperativeHandle(ref, () => ({
        prev: mocks.readerPrev,
        next: mocks.readerNext,
      }));
      useEffect(() => {
        onFileLoaded?.({ objectUrl: "blob:ebook", filename: "Reader.epub" });
        onProgressChange?.(0.421);
        return () => onFileLoaded?.(null);
      }, [onFileLoaded, onProgressChange]);
      return <div>reader surface {file.file_name}</div>;
    }),
  };
});

function makeVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: overrides.file_id ?? 7,
    file_name: overrides.file_name ?? "Reader.epub",
    file_path: overrides.file_path ?? "/books/reader.epub",
    resolution: "",
    codec_video: "",
    codec_audio: "",
    hdr: false,
    container: overrides.container ?? "epub",
    file_size: 100,
    duration: 0,
    bitrate: 0,
  };
}

function makeEbookItem(overrides: Partial<ItemDetail & { type: "ebook" }> = {}): ItemDetail & { type: "ebook" } {
  return {
    content_id: "ebook-1",
    type: "ebook",
    title: "Reader Book",
    original_title: "",
    year: 2026,
    overview: "",
    tagline: "",
    runtime: 0,
    content_rating: "",
    genres: [],
    rating_imdb: null,
    rating_tmdb: null,
    rating_rt_critic: null,
    rating_rt_audience: null,
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [],
    studios: [],
    networks: [],
    countries: [],
    release_date: null,
    first_air_date: null,
    last_air_date: null,
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
    season_count: null,
    series_id: "",
    series_title: "",
    season_number: null,
    episode_number: null,
    episode_count: null,
    air_date: null,
    is_specials: false,
    versions: [makeVersion()],
    subtitles: [],
    intro: null,
    credits: null,
    ...overrides,
  };
}

describe("EbookReader", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mocks.useCatalogItemDetail.mockReset();
    mocks.readerPrev.mockReset();
    mocks.readerNext.mockReset();
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem(),
      isLoading: false,
      error: null,
    });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  it("shows reader progress and wires page navigation controls", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    expect(container.textContent).toContain("42%");

    const previous = container.querySelector<HTMLButtonElement>('button[aria-label="Previous page"]');
    const next = container.querySelector<HTMLButtonElement>('button[aria-label="Next page"]');
    expect(previous).not.toBeNull();
    expect(next).not.toBeNull();

    await act(async () => {
      previous?.click();
      next?.click();
    });

    expect(mocks.readerPrev).toHaveBeenCalledTimes(1);
    expect(mocks.readerNext).toHaveBeenCalledTimes(1);
  });

  it("switches between multiple ebook files from the reader header", async () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem({
        versions: [
          makeVersion({ file_id: 8, file_name: "Reader.epub", container: "epub" }),
          makeVersion({ file_id: 9, file_name: "Reader.pdf", container: "pdf" }),
        ],
      }),
      isLoading: false,
      error: null,
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1?file_id=8"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    expect(container.textContent).toContain("reader surface Reader.epub");
    const select = container.querySelector<HTMLSelectElement>('select[aria-label="Reader file"]');
    expect(select).not.toBeNull();

    await act(async () => {
      if (!select) return;
      select.value = "9";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });

    expect(container.textContent).toContain("reader surface Reader.pdf");
  });

  it("only lists reader-supported files in the reader file selector", async () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem({
        versions: [
          makeVersion({ file_id: 8, file_name: "Reader.epub", container: "epub" }),
          makeVersion({ file_id: 9, file_name: "Reader.docx", container: "docx" }),
          makeVersion({ file_id: 10, file_name: "Reader.pdf", container: "pdf" }),
        ],
      }),
      isLoading: false,
      error: null,
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1?file_id=8"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const options = Array.from(container.querySelectorAll<HTMLOptionElement>("option")).map(
      (option) => option.textContent,
    );

    expect(options).toEqual(["EPUB · Reader.epub", "PDF · Reader.pdf"]);
  });
});
