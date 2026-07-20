// @vitest-environment jsdom

import { act, useEffect, useImperativeHandle, forwardRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FileVersion, ItemDetail } from "@/api/types";
import EbookReader from "./EbookReader";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
  useBookReadingStats: vi.fn(),
  readerPrev: vi.fn(),
  readerNext: vi.fn(),
  readerGoTo: vi.fn(),
  readerGoToFraction: vi.fn(),
  readerSearch: vi.fn(),
  hasLiveSelection: vi.fn(),
  captureReaderSettings: vi.fn(),
  captureCustomFontUrl: vi.fn(),
  fetchEbookReaderConfig: vi.fn(),
  saveEbookReaderConfig: vi.fn(),
  saveEbookReaderConfigKeepalive: vi.fn(),
  fetchEbookReaderAnnotations: vi.fn(),
  createEbookReaderAnnotation: vi.fn(),
  deleteEbookReaderAnnotation: vi.fn(),
  sendReadingHeartbeat: vi.fn(),
  fetchReaderFonts: vi.fn(),
  fetchReaderFontObjectUrl: vi.fn(),
  uploadReaderFont: vi.fn(),
  deleteReaderFont: vi.fn(),
  lastOnLocationChange: undefined as
    | ((info: { fraction: number; sectionIndex: number | null; tocLabel: string | null }) => void)
    | undefined,
  lastOnContentPointerUp: undefined as
    | ((point: { clientX: number; clientY: number }) => void)
    | undefined,
  lastOnContentPointerMove: undefined as ((point: { clientY: number }) => void) | undefined,
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: mocks.useCatalogItemDetail,
}));

vi.mock("@/hooks/queries/readingStats", () => ({
  useBookReadingStats: mocks.useBookReadingStats,
}));

vi.mock("@/components/PageBack", () => ({
  default: () => <div />,
}));

vi.mock("@/reader/ebookReaderApi", () => ({
  createEbookReaderAnnotation: mocks.createEbookReaderAnnotation,
  deleteEbookReaderAnnotation: mocks.deleteEbookReaderAnnotation,
  fetchEbookReaderAnnotations: mocks.fetchEbookReaderAnnotations,
  fetchEbookReaderConfig: mocks.fetchEbookReaderConfig,
  saveEbookReaderConfig: mocks.saveEbookReaderConfig,
  saveEbookReaderConfigKeepalive: mocks.saveEbookReaderConfigKeepalive,
  sendReadingHeartbeat: mocks.sendReadingHeartbeat,
}));

vi.mock("@/reader/readerFontsApi", () => ({
  fetchReaderFonts: mocks.fetchReaderFonts,
  fetchReaderFontObjectUrl: mocks.fetchReaderFontObjectUrl,
  uploadReaderFont: mocks.uploadReaderFont,
  deleteReaderFont: mocks.deleteReaderFont,
  readerFontFileUrl: (id: number) => `/api/v1/ebooks/reader-fonts/${id}/file`,
}));

vi.mock("@/reader/FoliateBookReader", async () => {
  const actual = await vi.importActual<typeof import("@/reader/FoliateBookReader")>(
    "@/reader/FoliateBookReader",
  );

  return {
    ...actual,
    default: forwardRef<
      {
        prev: () => void;
        next: () => void;
        goTo: (href: string) => void;
        goToFraction: (fraction: number) => Promise<void>;
        search: (
          query: string,
        ) => Promise<Array<{ cfi: string; label?: string; excerpt?: string }>>;
        clearSearch: () => void;
        clearSelection: () => void;
        createSelectionAnnotation: () => { cfi: string; selectedText: string } | null;
        getReadableText: () => string;
        getSectionFractions: () => number[];
        hasLiveSelection: () => boolean;
      },
      {
        file: FileVersion;
        settings?: unknown;
        customFontUrl?: string | null;
        annotations?: unknown[];
        onProgressChange?: (progress: number | null) => void;
        onFileLoaded?: (state: { objectUrl: string; filename: string } | null) => void;
        onSelectionChange?: (selection: { cfi: string; selectedText: string } | null) => void;
        onLocationChange?: (info: {
          fraction: number;
          sectionIndex: number | null;
          tocLabel: string | null;
        }) => void;
        onContentPointerUp?: (point: { clientX: number; clientY: number }) => void;
        onContentPointerMove?: (point: { clientY: number }) => void;
        onReady?: (state: {
          toc: Array<{
            id: number;
            label: string;
            href: string;
            index: number;
            subitems?: Array<{ id: number; label: string; href: string; index: number }>;
          }>;
        }) => void;
      }
    >(function MockFoliateBookReader(
      {
        file,
        settings,
        customFontUrl,
        onProgressChange,
        onFileLoaded,
        onSelectionChange,
        onLocationChange,
        onContentPointerUp,
        onContentPointerMove,
        onReady,
      },
      ref,
    ) {
      mocks.captureReaderSettings(settings);
      mocks.captureCustomFontUrl(customFontUrl);
      useEffect(() => {
        mocks.lastOnLocationChange = onLocationChange;
      }, [onLocationChange]);
      useEffect(() => {
        mocks.lastOnContentPointerUp = onContentPointerUp;
      }, [onContentPointerUp]);
      useEffect(() => {
        mocks.lastOnContentPointerMove = onContentPointerMove;
      }, [onContentPointerMove]);
      useImperativeHandle(ref, () => ({
        prev: mocks.readerPrev,
        next: mocks.readerNext,
        goTo: mocks.readerGoTo,
        goToFraction: mocks.readerGoToFraction,
        search: mocks.readerSearch,
        clearSearch: vi.fn(),
        clearSelection: () => onSelectionChange?.(null),
        createSelectionAnnotation: () => ({
          cfi: "epubcfi(/6/4,/1:0,/1:12)",
          selectedText: "sample text",
        }),
        getReadableText: () => "Readable text for speech",
        getSectionFractions: () => [0, 0.25, 0.6, 1],
        hasLiveSelection: mocks.hasLiveSelection,
      }));
      useEffect(() => {
        onFileLoaded?.({ objectUrl: "blob:ebook", filename: "Reader.epub" });
        onProgressChange?.(0.421);
        onSelectionChange?.({
          cfi: "epubcfi(/6/4,/1:0,/1:12)",
          selectedText: "sample text",
        });
        onReady?.({
          toc: [
            {
              id: 1,
              label: "Opening",
              href: "chapter-1.xhtml",
              index: 0,
              subitems: [{ id: 2, label: "Aboard", href: "chapter-1.xhtml#aboard", index: 0 }],
            },
          ],
        });
        return () => onFileLoaded?.(null);
      }, [onFileLoaded, onProgressChange, onReady, onSelectionChange]);
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

function makeEbookItem(
  overrides: Partial<ItemDetail & { type: "ebook" }> = {},
): ItemDetail & { type: "ebook" } {
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

function installStorage() {
  const values = new Map<string, string>();
  const storage = {
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => values.set(key, value)),
    removeItem: vi.fn((key: string) => values.delete(key)),
    clear: vi.fn(() => values.clear()),
    key: vi.fn((index: number) => Array.from(values.keys())[index] ?? null),
    get length() {
      return values.size;
    },
  } as Storage;
  Object.defineProperty(window, "localStorage", {
    value: storage,
    configurable: true,
  });
  Object.defineProperty(globalThis, "localStorage", {
    value: storage,
    configurable: true,
  });
  return storage;
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function clickThemeSwatch(container: HTMLDivElement, name: string) {
  container.querySelector<HTMLButtonElement>(`[data-theme-choice="${name}"]`)?.click();
}

function clickDarkModeToggle(container: HTMLDivElement) {
  container.querySelector<HTMLButtonElement>('[aria-label="Reader dark mode"]')?.click();
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
    installStorage();
    mocks.useCatalogItemDetail.mockReset();
    mocks.useBookReadingStats.mockReset();
    mocks.readerPrev.mockReset();
    mocks.readerNext.mockReset();
    mocks.readerGoTo.mockReset();
    mocks.readerGoToFraction.mockReset();
    mocks.readerSearch.mockReset();
    mocks.hasLiveSelection.mockReset();
    mocks.hasLiveSelection.mockReturnValue(false);
    mocks.captureReaderSettings.mockReset();
    mocks.captureCustomFontUrl.mockReset();
    mocks.fetchEbookReaderConfig.mockReset();
    mocks.saveEbookReaderConfig.mockReset();
    mocks.saveEbookReaderConfigKeepalive.mockReset();
    mocks.fetchEbookReaderAnnotations.mockReset();
    mocks.createEbookReaderAnnotation.mockReset();
    mocks.deleteEbookReaderAnnotation.mockReset();
    mocks.sendReadingHeartbeat.mockReset();
    mocks.fetchReaderFonts.mockReset();
    mocks.fetchReaderFontObjectUrl.mockReset();
    mocks.uploadReaderFont.mockReset();
    mocks.deleteReaderFont.mockReset();
    mocks.fetchReaderFonts.mockResolvedValue([]);
    mocks.fetchReaderFontObjectUrl.mockImplementation(async (id: number) => `blob:mock-font-${id}`);
    URL.revokeObjectURL = vi.fn();
    mocks.readerSearch.mockResolvedValue([
      { cfi: "epubcfi(/6/8)", label: "Chapter 2", excerpt: "Shanghai harbor" },
    ]);
    mocks.fetchEbookReaderConfig.mockResolvedValue({});
    mocks.saveEbookReaderConfig.mockResolvedValue({});
    mocks.fetchEbookReaderAnnotations.mockResolvedValue([]);
    mocks.createEbookReaderAnnotation.mockResolvedValue({
      id: "ann-2",
      content_id: "ebook-1",
      kind: "highlight",
      cfi_range: "epubcfi(/6/4,/1:0,/1:12)",
      selected_text: "sample text",
      note: "",
      style: "highlight",
      color: "#facc15",
    });
    mocks.deleteEbookReaderAnnotation.mockResolvedValue(undefined);
    mocks.sendReadingHeartbeat.mockResolvedValue(undefined);
    localStorage.clear();
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem(),
      isLoading: false,
      error: null,
    });
    mocks.useBookReadingStats.mockReturnValue({ data: undefined });
  });

  afterEach(async () => {
    vi.useRealTimers();
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

    const previous = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Previous page"]',
    );
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

  it("shows the time remaining in the footer once reading stats resolve", async () => {
    mocks.useBookReadingStats.mockReturnValue({
      data: { pace_fraction_per_hour: 0.1, time_left_seconds: 7800, book_seconds: 36000 },
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    expect(container.textContent).toContain("2h 10m left");
    expect(mocks.useBookReadingStats).toHaveBeenCalledWith("ebook-1");
  });

  it("omits the time remaining when reading stats haven't resolved", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    expect(container.textContent).not.toContain("left");
  });

  it("preserves library context on the back-to-ebook link", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1?libraryId=12"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    expect(container.innerHTML).toContain('href="/item/ebook-1?libraryId=12"');
  });

  it("sends the reader back action to an explicit backTo target (manga series)", async () => {
    const backTo = encodeURIComponent("/item/manga-series-1?libraryId=7");
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[`/reader/ebook/ebook-1?libraryId=7&backTo=${backTo}`]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    // backTo wins over the default chapter-detail target, breaking the loop.
    expect(container.innerHTML).toContain('href="/item/manga-series-1?libraryId=7"');
    expect(container.innerHTML).not.toContain('href="/item/ebook-1?libraryId=7"');
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

  it("falls back to a supported reader file when the requested file is unsupported", async () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem({
        versions: [
          makeVersion({ file_id: 8, file_name: "Reader.epub", container: "epub" }),
          makeVersion({ file_id: 9, file_name: "Reader.docx", container: "docx" }),
        ],
      }),
      isLoading: false,
      error: null,
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1?file_id=9"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    expect(container.textContent).toContain("reader surface Reader.epub");
    expect(container.textContent).not.toContain("Unsupported ebook format.");
  });

  it("shows the table of contents and navigates to a selected section", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    expect(container.textContent).toContain("Opening");
    expect(container.textContent).toContain("Aboard");

    const aboard = Array.from(container.querySelectorAll<HTMLButtonElement>("button")).find(
      (button) => button.textContent === "Aboard",
    );

    await act(async () => {
      aboard?.click();
    });

    expect(mocks.readerGoTo).toHaveBeenCalledWith("chapter-1.xhtml#aboard");
  });

  it("searches inside the reader and navigates to a selected result", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const searchTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Search book"]',
    );
    await act(async () => {
      searchTab?.click();
    });

    const input = container.querySelector<HTMLInputElement>('input[aria-label="Search text"]');
    await act(async () => {
      if (!input) return;
      setInputValue(input, "Shanghai");
    });

    const submit = container.querySelector<HTMLButtonElement>('button[aria-label="Run search"]');
    await act(async () => {
      submit?.click();
    });

    expect(mocks.readerSearch).toHaveBeenCalledWith("Shanghai");
    expect(container.textContent).toContain("Shanghai harbor");

    const result = Array.from(container.querySelectorAll<HTMLButtonElement>("button")).find(
      (button) => button.textContent?.includes("Shanghai harbor"),
    );
    await act(async () => {
      result?.click();
    });

    expect(mocks.readerGoTo).toHaveBeenCalledWith("epubcfi(/6/8)");
  });

  it("loads server reader settings and passes them to the reader", async () => {
    mocks.fetchEbookReaderConfig.mockResolvedValue({
      settings: { theme: "sepia", fontSize: 130 },
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.fetchEbookReaderConfig).toHaveBeenCalledWith("ebook-1");
    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ theme: "sepia", fontSize: 130 }),
    );
  });

  it("persists reader settings to the server and local fallback", async () => {
    vi.useFakeTimers();

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    await act(async () => {
      clickThemeSwatch(container, "default");
      clickDarkModeToggle(container);
    });

    await act(async () => {
      vi.advanceTimersByTime(450);
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ theme: "dark" }),
    );
    expect(localStorage.getItem("silo.ebook.reader.settings")).toContain('"theme":"dark"');
    expect(mocks.saveEbookReaderConfig).toHaveBeenCalledWith(
      "ebook-1",
      expect.objectContaining({
        settings: expect.objectContaining({ theme: "dark" }),
      }),
    );

    vi.useRealTimers();
  });

  it("flushes a pending debounced settings save when unmounting inside the debounce window", async () => {
    vi.useFakeTimers();

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    await act(async () => {
      clickThemeSwatch(container, "default");
      clickDarkModeToggle(container);
    });
    expect(mocks.saveEbookReaderConfig).not.toHaveBeenCalled();

    // Unmount before the 400ms debounce elapses (quick SPA navigation).
    await act(async () => {
      root.unmount();
    });

    expect(mocks.saveEbookReaderConfig).toHaveBeenCalledWith(
      "ebook-1",
      expect.objectContaining({
        settings: expect.objectContaining({ theme: "dark" }),
      }),
    );

    vi.useRealTimers();
  });

  it("flushes a pending debounced settings save with keepalive on pagehide, exactly once", async () => {
    vi.useFakeTimers();

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    await act(async () => {
      clickThemeSwatch(container, "sepia");
    });

    await act(async () => {
      window.dispatchEvent(new Event("pagehide"));
    });

    expect(mocks.saveEbookReaderConfigKeepalive).toHaveBeenCalledTimes(1);
    expect(mocks.saveEbookReaderConfigKeepalive).toHaveBeenCalledWith(
      "ebook-1",
      expect.objectContaining({
        settings: expect.objectContaining({ theme: "sepia" }),
      }),
    );

    // The pending save was consumed: neither the debounce timer firing nor the
    // unmount flush may send it again.
    await act(async () => {
      vi.advanceTimersByTime(450);
    });
    await act(async () => {
      root.unmount();
    });
    expect(mocks.saveEbookReaderConfig).not.toHaveBeenCalled();
    expect(mocks.saveEbookReaderConfigKeepalive).toHaveBeenCalledTimes(1);

    vi.useRealTimers();
  });

  it("resets reader settings to stock defaults", async () => {
    vi.useFakeTimers();
    mocks.fetchEbookReaderConfig.mockResolvedValue({
      settings: { theme: "dark", fontSize: 140, flow: "scrolled" },
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    await act(async () => {
      await Promise.resolve();
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    const reset = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reset reader settings"]',
    );
    await act(async () => {
      reset?.click();
      vi.advanceTimersByTime(450);
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ theme: "light", fontSize: 112, flow: "paginated" }),
    );
    expect(mocks.saveEbookReaderConfig).toHaveBeenCalledWith(
      "ebook-1",
      expect.objectContaining({
        settings: expect.objectContaining({ theme: "light", fontSize: 112, flow: "paginated" }),
      }),
    );
    expect(localStorage.getItem("silo.ebook.reader.settings")).toContain('"theme":"light"');

    vi.useRealTimers();
  });

  it("offers reliable font choices and reading profile presets", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    const font = container.querySelector<HTMLSelectElement>('select[aria-label="Font family"]');
    const fontOptions = Array.from(font?.options ?? []).map((option) => option.textContent ?? "");
    expect(fontOptions).toContain("Book default");
    expect(fontOptions).toContain("System serif");
    expect(fontOptions).toContain("System sans");
    expect(fontOptions).not.toContain("Inter");
    expect(fontOptions).not.toContain("Merriweather");

    const comfortable = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Comfortable"),
    );
    await act(async () => {
      comfortable?.click();
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({
        fontFamily: 'ui-serif, Georgia, Cambria, "Times New Roman", Times, serif',
        fontSize: 112,
        lineHeight: 1.75,
      }),
    );
    expect(comfortable?.getAttribute("aria-pressed")).toBe("true");
  });

  it("renders the theme grid and switches palette pairs", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    const ocean = container.querySelector<HTMLButtonElement>('[data-theme-choice="ocean"]');
    expect(ocean).not.toBeNull();
    await act(async () => {
      ocean?.click();
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ themeName: "ocean", theme: "light" }),
    );

    await act(async () => {
      clickDarkModeToggle(container);
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ themeName: "ocean", themeVariant: "dark", theme: "dark" }),
    );
  });

  it("disables the variant toggle for AMOLED", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    await act(async () => {
      clickThemeSwatch(container, "amoled");
    });

    const toggle = container.querySelector<HTMLButtonElement>('[aria-label="Reader dark mode"]');
    expect(toggle?.disabled).toBe(true);
  });

  it("offers uploaded fonts in the font picker and falls back on delete", async () => {
    mocks.fetchReaderFonts.mockResolvedValue([
      { id: 3, name: "Literata", filename: "l.woff2", created_at: "" },
    ]);
    mocks.deleteReaderFont.mockResolvedValue(undefined);

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const font = container.querySelector<HTMLSelectElement>('select[aria-label="Font family"]');
    const uploadedGroup = font?.querySelector('optgroup[label="Uploaded"]');
    expect(uploadedGroup).not.toBeNull();
    const option = uploadedGroup?.querySelector<HTMLOptionElement>('option[value="custom:3"]');
    expect(option?.textContent).toBe("Literata");

    await act(async () => {
      if (!font) return;
      font.value = "custom:3";
      font.dispatchEvent(new Event("change", { bubbles: true }));
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ fontFamily: "custom", customFontID: 3 }),
    );

    await act(async () => {
      await Promise.resolve();
    });

    // The reader must receive an authenticated blob: URL for the font, never
    // the raw API path (a browser-native @font-face fetch can't carry auth).
    expect(mocks.fetchReaderFontObjectUrl).toHaveBeenCalledWith(3);
    expect(mocks.captureCustomFontUrl).toHaveBeenLastCalledWith("blob:mock-font-3");

    const deleteButton = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Delete font Literata"]',
    );
    await act(async () => {
      deleteButton?.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.deleteReaderFont).toHaveBeenCalledWith(3);
    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ fontFamily: "inherit", customFontID: null }),
    );
    // Falling back off the deleted font must revoke the blob: URL it created.
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:mock-font-3");
    expect(mocks.captureCustomFontUrl).toHaveBeenLastCalledWith(null);
  });

  it("falls back to the book's own typeface when the font blob fetch fails", async () => {
    mocks.fetchReaderFonts.mockResolvedValue([
      { id: 3, name: "Literata", filename: "l.woff2", created_at: "" },
    ]);
    mocks.fetchReaderFontObjectUrl.mockRejectedValue(new Error("network error"));

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const font = container.querySelector<HTMLSelectElement>('select[aria-label="Font family"]');
    await act(async () => {
      if (!font) return;
      font.value = "custom:3";
      font.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.fetchReaderFontObjectUrl).toHaveBeenCalledWith(3);
    // A rejected fetch must leave the font url null rather than passing a
    // stale/undefined value through to the reader's CSS.
    expect(mocks.captureCustomFontUrl).toHaveBeenLastCalledWith(null);
  });

  it("shows a loading placeholder instead of a blank select while a saved custom font hasn't loaded yet", async () => {
    mocks.fetchEbookReaderConfig.mockResolvedValue({
      settings: { customFontID: 3, fontFamily: "custom" },
    });
    // Never resolves within this test, simulating the fonts list still loading.
    mocks.fetchReaderFonts.mockReturnValue(new Promise(() => {}));

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const font = container.querySelector<HTMLSelectElement>('select[aria-label="Font family"]');
    expect(font?.value).toBe("custom:3");
    const placeholder = font?.querySelector<HTMLOptionElement>('option[value="custom:3"]');
    expect(placeholder).not.toBeNull();
    expect(placeholder?.disabled).toBe(true);
    expect(placeholder?.textContent).toBe("Loading uploaded font…");
  });

  it("keeps a saved custom font selection through a transient fonts-list fetch failure", async () => {
    mocks.fetchReaderFonts.mockReset();
    mocks.fetchReaderFonts.mockRejectedValue(new Error("network error"));
    mocks.fetchEbookReaderConfig.mockResolvedValue({
      settings: { customFontID: 3, fontFamily: "custom" },
    });
    vi.useFakeTimers();

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    await act(async () => {
      await Promise.resolve();
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    // Let the rejected fetchReaderFonts() promise settle.
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    await act(async () => {
      vi.advanceTimersByTime(1000);
    });

    expect(mocks.saveEbookReaderConfig).not.toHaveBeenCalledWith(
      "ebook-1",
      expect.objectContaining({
        settings: expect.objectContaining({ customFontID: null }),
      }),
    );

    vi.useRealTimers();
  });

  it("retries the fonts fetch after a failure the next time the panel opens", async () => {
    mocks.fetchReaderFonts.mockReset();
    mocks.fetchReaderFonts.mockRejectedValueOnce(new Error("network error"));
    mocks.fetchReaderFonts.mockResolvedValueOnce([
      { id: 3, name: "Literata", filename: "l.woff2", created_at: "" },
    ]);

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    const tocTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Table of contents"]',
    );
    await act(async () => {
      settingsTab?.click();
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.fetchReaderFonts).toHaveBeenCalledTimes(1);

    await act(async () => {
      tocTab?.click();
    });
    await act(async () => {
      settingsTab?.click();
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.fetchReaderFonts).toHaveBeenCalledTimes(2);

    const font = container.querySelector<HTMLSelectElement>('select[aria-label="Font family"]');
    const uploadedGroup = font?.querySelector('optgroup[label="Uploaded"]');
    expect(uploadedGroup).not.toBeNull();
  });

  it("skips the fonts fetch entirely for comic formats", async () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem({
        versions: [makeVersion({ file_id: 8, file_name: "Comic.cbz", container: "cbz" })],
      }),
      isLoading: false,
      error: null,
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.fetchReaderFonts).not.toHaveBeenCalled();
  });

  it("surfaces a delete-font failure as an inline error instead of an unhandled rejection", async () => {
    mocks.fetchReaderFonts.mockResolvedValue([
      { id: 3, name: "Literata", filename: "l.woff2", created_at: "" },
    ]);
    mocks.deleteReaderFont.mockRejectedValue(new Error("boom"));

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const deleteButton = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Delete font Literata"]',
    );
    await act(async () => {
      deleteButton?.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.deleteReaderFont).toHaveBeenCalledWith(3);
    // The font must still be listed — the optimistic removal never happened.
    const uploadedGroup = container
      .querySelector<HTMLSelectElement>('select[aria-label="Font family"]')
      ?.querySelector('optgroup[label="Uploaded"]');
    expect(uploadedGroup?.querySelector('option[value="custom:3"]')).not.toBeNull();
    expect(container.textContent).toContain("Font delete failed.");
  });

  it("toggles the persisted reading ruler overlay", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const ruler = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Toggle reading ruler"]',
    );
    await act(async () => {
      ruler?.click();
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ readingRuler: true }),
    );
    const handle = container.querySelector('[role="slider"][aria-label="Reading ruler position"]');
    expect(handle).not.toBeNull();
    expect(handle?.getAttribute("aria-valuenow")).toBe("50");
  });

  it("does not turn the page when releasing the reading-ruler drag", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const ruler = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Toggle reading ruler"]',
    );
    await act(async () => {
      ruler?.click();
    });

    const surface = container.querySelector("[data-reader-surface]") as HTMLElement;
    surface.getBoundingClientRect = () =>
      ({
        left: 0,
        width: 300,
        top: 0,
        height: 500,
        right: 300,
        bottom: 500,
        x: 0,
        y: 0,
        toJSON() {
          return {};
        },
      }) as DOMRect;

    const grip = container.querySelector<HTMLButtonElement>(
      '[role="slider"][aria-label="Reading ruler position"]',
    )!;
    // jsdom has no pointer-capture implementation; the grip's handlers call
    // these unconditionally, so stub them the way a real button would provide.
    grip.setPointerCapture = vi.fn();
    grip.releasePointerCapture = vi.fn();

    act(() => {
      grip.dispatchEvent(
        new PointerEvent("pointerdown", { clientY: 250, pointerId: 1, bubbles: true }),
      );
    });
    act(() => {
      // The grip sits near the reading surface's right edge; if this pointerup
      // bubbles up to the surface's tap handler it reads as a next-page tap.
      grip.dispatchEvent(
        new PointerEvent("pointerup", { clientY: 250, pointerId: 1, bubbles: true }),
      );
    });

    expect(mocks.readerNext).not.toHaveBeenCalled();
  });

  it("constrains the reader grid so the side panel stays inside the viewport", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const main = container.querySelector("main");
    const readerPane = main?.querySelector("section");
    const sidePanel = main?.querySelector("aside");

    expect(main?.className).toContain("overflow-hidden");
    expect(readerPane?.className).toContain("min-w-0");
    expect(sidePanel?.className).toContain("min-w-0");
  });

  it("renders the chapter-aware footer instead of a header slider", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    act(() => {
      mocks.lastOnLocationChange?.({ fraction: 0.3, sectionIndex: 1, tocLabel: "Chapter 2" });
    });

    const header = container.querySelector("header")!;
    expect(header.querySelector('input[type="range"]')).toBeNull();
    const footer = container.querySelector("footer")!;
    expect(footer.textContent).toContain("Chapter 2");
    expect(footer.textContent).toContain("30%");
    expect(footer.querySelector("[data-chapter-band]")).not.toBeNull();
  });

  it("sends a reading heartbeat on relocate, then again every 30s", async () => {
    vi.useFakeTimers();

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    // Relocate is one of the activity signals: it should beat immediately
    // rather than waiting up to 30s for the first tick.
    act(() => {
      mocks.lastOnLocationChange?.({ fraction: 0.3, sectionIndex: 1, tocLabel: "Chapter 2" });
    });
    expect(mocks.sendReadingHeartbeat).toHaveBeenCalledWith("ebook-1", 0.3);

    mocks.sendReadingHeartbeat.mockClear();
    await act(async () => {
      vi.advanceTimersByTime(30_000);
    });
    expect(mocks.sendReadingHeartbeat).toHaveBeenCalledWith("ebook-1", 0.3);

    vi.useRealTimers();
  });

  it("does not send a heartbeat or enable the reading-stats query while the item detail is still loading", async () => {
    // isComicFormat is derived from the loaded item's file versions, so
    // while useCatalogItemDetail is still pending it's false — the same as
    // a prose book. Without gating on the item being loaded, a keypress
    // during this window would fire a stats GET and a spurious heartbeat
    // for a book that might turn out to be a comic (which the reader must
    // never track).
    vi.useFakeTimers();
    mocks.useCatalogItemDetail.mockReturnValue({
      data: undefined,
      isLoading: true,
      error: null,
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
      vi.advanceTimersByTime(60_000);
    });

    expect(mocks.sendReadingHeartbeat).not.toHaveBeenCalled();
    const lastReadingStatsCall =
      mocks.useBookReadingStats.mock.calls[mocks.useBookReadingStats.mock.calls.length - 1];
    expect(lastReadingStatsCall?.[0]).toBeUndefined();

    vi.useRealTimers();
  });

  it("scrubs reader progress and supports keyboard page navigation", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const scrubber = container.querySelector<HTMLInputElement>(
      'input[aria-label="Reading progress"]',
    );
    await act(async () => {
      if (!scrubber) return;
      setInputValue(scrubber, "65");
    });

    expect(mocks.readerGoToFraction).toHaveBeenCalledWith(0.65);

    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft" }));
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    });

    expect(mocks.readerPrev).toHaveBeenCalledTimes(1);
    expect(mocks.readerNext).toHaveBeenCalledTimes(1);
  });

  it("does not snap the progress bar back after scrubbing", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    // A prior relocate reported a stale fraction (30%); the footer prefers
    // locationInfo.fraction over readerProgress, so scrubbing must update it
    // too or the bar snaps back to 30% on the next render.
    act(() => {
      mocks.lastOnLocationChange?.({ fraction: 0.3, sectionIndex: 1, tocLabel: "Chapter 2" });
    });

    const scrubber = container.querySelector<HTMLInputElement>(
      'input[aria-label="Reading progress"]',
    );
    await act(async () => {
      if (!scrubber) return;
      setInputValue(scrubber, "65");
    });

    expect(scrubber?.value).toBe("65");
    expect(container.querySelector("footer")?.textContent).toContain("65%");
  });

  it("pages on edge taps and toggles chrome on middle tap", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    // The mock reader reports an active selection on mount; clear it via the
    // highlight action so tap zones are free to page/toggle chrome.
    const highlight = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Highlight selection"]',
    );
    await act(async () => {
      highlight?.click();
    });

    const surface = container.querySelector("[data-reader-surface]") as HTMLElement;
    expect(surface).not.toBeNull();
    surface.getBoundingClientRect = () =>
      ({
        left: 0,
        width: 300,
        top: 0,
        height: 500,
        right: 300,
        bottom: 500,
        x: 0,
        y: 0,
        toJSON() {
          return {};
        },
      }) as DOMRect;

    act(() => {
      surface.dispatchEvent(new MouseEvent("pointerup", { clientX: 30, bubbles: true }));
    });
    expect(mocks.readerPrev).toHaveBeenCalled();

    act(() => {
      surface.dispatchEvent(new MouseEvent("pointerup", { clientX: 270, bubbles: true }));
    });
    expect(mocks.readerNext).toHaveBeenCalled();

    expect(container.querySelector("header")).not.toBeNull();
    act(() => {
      surface.dispatchEvent(new MouseEvent("pointerup", { clientX: 150, bubbles: true }));
    });
    expect(container.querySelector("header")).toBeNull();
    expect(container.querySelector("footer")).toBeNull();
    act(() => {
      surface.dispatchEvent(new MouseEvent("pointerup", { clientX: 150, bubbles: true }));
    });
    expect(container.querySelector("header")).not.toBeNull();
  });

  it("does not page-turn an edge tap that just finished a selection, even though React selection state already reads null", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    // Clear the selection the mock reports on mount so React's `selection`
    // state is null, mirroring the race: the pointerup ending a selection
    // fires before FoliateBookReader's deferred onSelectionChange lands, so
    // by the time dispatchSurfaceTap runs, `selection` is stale/null.
    const highlight = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Highlight selection"]',
    );
    await act(async () => {
      highlight?.click();
    });

    // The reader's synchronous live-selection signal still reports true —
    // this is what dispatchSurfaceTap must also consult.
    mocks.hasLiveSelection.mockReturnValue(true);

    const surface = container.querySelector("[data-reader-surface]") as HTMLElement;
    expect(surface).not.toBeNull();
    surface.getBoundingClientRect = () =>
      ({
        left: 0,
        width: 300,
        top: 0,
        height: 500,
        right: 300,
        bottom: 500,
        x: 0,
        y: 0,
        toJSON() {
          return {};
        },
      }) as DOMRect;

    act(() => {
      // An edge tap, which would otherwise page forward.
      surface.dispatchEvent(new MouseEvent("pointerup", { clientX: 270, bubbles: true }));
    });

    expect(mocks.readerNext).not.toHaveBeenCalled();
  });

  // FoliateBookReader is mocked in this file, so these tests only exercise
  // EbookReader's own dispatch logic once it receives already-converted
  // viewport coordinates from onContentPointerUp/onContentPointerMove. The
  // real bridging across the content iframe boundary — translating iframe
  // pointer/mousemove events into those viewport coordinates via the frame
  // element's getBoundingClientRect() — is covered by the component test in
  // FoliateBookReader.component.test.tsx ("forwards pointerup and mousemove
  // from content iframes with viewport-adjusted coordinates").
  it("pages on edge taps and toggles chrome on middle tap reported through the content pointer bridge", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    // The mock reader reports an active selection on mount; clear it via the
    // highlight action so tap zones are free to page/toggle chrome.
    const highlight = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Highlight selection"]',
    );
    await act(async () => {
      highlight?.click();
    });

    const surface = container.querySelector("[data-reader-surface]") as HTMLElement;
    expect(surface).not.toBeNull();
    surface.getBoundingClientRect = () =>
      ({
        left: 0,
        width: 300,
        top: 0,
        height: 500,
        right: 300,
        bottom: 500,
        x: 0,
        y: 0,
        toJSON() {
          return {};
        },
      }) as DOMRect;

    expect(mocks.lastOnContentPointerUp).toBeTypeOf("function");
    expect(mocks.lastOnContentPointerMove).toBeTypeOf("function");

    act(() => {
      mocks.lastOnContentPointerUp?.({ clientX: 30, clientY: 10 });
    });
    expect(mocks.readerPrev).toHaveBeenCalled();

    act(() => {
      mocks.lastOnContentPointerUp?.({ clientX: 270, clientY: 10 });
    });
    expect(mocks.readerNext).toHaveBeenCalled();

    expect(container.querySelector("header")).not.toBeNull();
    act(() => {
      mocks.lastOnContentPointerUp?.({ clientX: 150, clientY: 10 });
    });
    // Middle tap toggled chrome off.
    expect(container.querySelector("header")).toBeNull();
    expect(container.querySelector("footer")).toBeNull();

    // With chrome hidden, a content mousemove near the top viewport edge
    // (converted to outer coordinates by FoliateBookReader in real usage)
    // must reveal chrome again, mirroring the window-level mousemove path.
    act(() => {
      mocks.lastOnContentPointerMove?.({ clientY: 500 });
    });
    expect(container.querySelector("header")).toBeNull();

    act(() => {
      mocks.lastOnContentPointerMove?.({ clientY: 5 });
    });
    expect(container.querySelector("header")).not.toBeNull();
  });

  it("always shows chrome and ignores tap zones for comic formats", async () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem({
        versions: [makeVersion({ file_id: 8, file_name: "Comic.cbz", container: "cbz" })],
      }),
      isLoading: false,
      error: null,
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const surface = container.querySelector("[data-reader-surface]") as HTMLElement;
    expect(surface).not.toBeNull();
    surface.getBoundingClientRect = () =>
      ({
        left: 0,
        width: 300,
        top: 0,
        height: 500,
        right: 300,
        bottom: 500,
        x: 0,
        y: 0,
        toJSON() {
          return {};
        },
      }) as DOMRect;

    expect(container.querySelector("header")).not.toBeNull();
    act(() => {
      surface.dispatchEvent(new MouseEvent("pointerup", { clientX: 150, bubbles: true }));
    });
    // Comics never hide chrome, and the tap-zone handler is inert for them.
    expect(container.querySelector("header")).not.toBeNull();
    expect(mocks.readerPrev).not.toHaveBeenCalled();
    expect(mocks.readerNext).not.toHaveBeenCalled();
  });

  it("loads annotations, creates highlights, and deletes annotations", async () => {
    mocks.fetchEbookReaderAnnotations.mockResolvedValue([
      {
        id: "ann-1",
        content_id: "ebook-1",
        kind: "bookmark",
        location: "epubcfi(/6/8)",
        selected_text: "",
        note: "Saved spot",
        style: "highlight",
        color: "#facc15",
      },
    ]);

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    await act(async () => {
      await Promise.resolve();
    });

    const notesTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Annotations and bookmarks"]',
    );
    await act(async () => {
      notesTab?.click();
    });

    expect(container.textContent).toContain("Saved spot");

    const deleteButton = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Delete annotation"]',
    );
    await act(async () => {
      deleteButton?.click();
    });

    expect(mocks.deleteEbookReaderAnnotation).toHaveBeenCalledWith("ebook-1", "ann-1");

    const highlight = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Highlight selection"]',
    );
    await act(async () => {
      highlight?.click();
    });

    expect(mocks.createEbookReaderAnnotation).toHaveBeenCalledWith(
      "ebook-1",
      expect.objectContaining({
        kind: "highlight",
        cfi_range: "epubcfi(/6/4,/1:0,/1:12)",
        selected_text: "sample text",
      }),
    );
  });

  it("navigates fraction bookmarks through goToFraction and CFI annotations through goTo", async () => {
    mocks.fetchEbookReaderAnnotations.mockResolvedValue([
      {
        id: "ann-frac",
        content_id: "ebook-1",
        kind: "bookmark",
        location: "fraction:0.250000",
        selected_text: "",
        note: "Fraction bookmark",
        style: "",
        color: "",
      },
      {
        id: "ann-cfi",
        content_id: "ebook-1",
        kind: "highlight",
        cfi_range: "epubcfi(/6/12)",
        selected_text: "CFI highlight",
        note: "",
        style: "highlight",
        color: "#facc15",
      },
    ]);

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const notesTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Annotations and bookmarks"]',
    );
    await act(async () => {
      notesTab?.click();
    });

    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>("button"));
    const fractionBookmark = buttons.find((button) =>
      button.textContent?.includes("Fraction bookmark"),
    );
    await act(async () => {
      fractionBookmark?.click();
    });

    expect(mocks.readerGoToFraction).toHaveBeenCalledWith(0.25);
    expect(mocks.readerGoTo).not.toHaveBeenCalled();

    const cfiHighlight = buttons.find((button) => button.textContent?.includes("CFI highlight"));
    await act(async () => {
      cfiHighlight?.click();
    });

    expect(mocks.readerGoTo).toHaveBeenCalledWith("epubcfi(/6/12)");
  });

  it("keeps settings changed before the server config arrives and persists them", async () => {
    let resolveConfig!: (config: Record<string, unknown>) => void;
    mocks.fetchEbookReaderConfig.mockReturnValue(
      new Promise<Record<string, unknown>>((resolve) => {
        resolveConfig = resolve;
      }),
    );

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    await act(async () => {
      clickThemeSwatch(container, "default");
      clickDarkModeToggle(container);
    });

    await act(async () => {
      resolveConfig({ settings: { theme: "sepia", fontSize: 130 } });
    });

    // The late server config must not clobber the user's in-flight change...
    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ theme: "dark" }),
    );
    // ...and the user's settings win on the server too.
    expect(mocks.saveEbookReaderConfig).toHaveBeenCalledWith(
      "ebook-1",
      expect.objectContaining({
        settings: expect.objectContaining({ theme: "dark" }),
      }),
    );
  });

  it("renders search results with duplicate CFIs without key collisions", async () => {
    mocks.readerSearch.mockResolvedValue([
      { cfi: "epubcfi(/6/8)", excerpt: "first match" },
      { cfi: "epubcfi(/6/8)", excerpt: "second match" },
    ]);
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});

    try {
      await act(async () => {
        root.render(
          <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
            <Routes>
              <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
            </Routes>
          </MemoryRouter>,
        );
      });

      const searchTab = container.querySelector<HTMLButtonElement>(
        'button[aria-label="Search book"]',
      );
      await act(async () => {
        searchTab?.click();
      });

      const input = container.querySelector<HTMLInputElement>('input[aria-label="Search text"]');
      await act(async () => {
        if (!input) return;
        setInputValue(input, "match");
      });

      const submit = container.querySelector<HTMLButtonElement>('button[aria-label="Run search"]');
      await act(async () => {
        submit?.click();
      });

      expect(container.textContent).toContain("first match");
      expect(container.textContent).toContain("second match");
      const keyWarnings = consoleError.mock.calls.filter((call) =>
        String(call[0]).includes("same key"),
      );
      expect(keyWarnings).toEqual([]);
    } finally {
      consoleError.mockRestore();
    }
  });

  it("shows read aloud and reading aid controls", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    expect(container.querySelector('button[aria-label="Speak text"]')).not.toBeNull();
    expect(container.querySelector('input[aria-label="Keep screen awake"]')).not.toBeNull();
    expect(container.querySelector('input[aria-label="E-ink mode"]')).toBeNull();
  });

  it("shows useful advanced reader controls without diagnostics UI or no-op controls", async () => {
    vi.useFakeTimers();

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    expect(container.querySelector('[aria-label="Diagnostics"]')).toBeNull();
    expect(container.textContent).not.toContain("Diagnostics");

    const brightness = container.querySelector<HTMLInputElement>('input[aria-label="Brightness"]');
    const hyphenation = container.querySelector<HTMLInputElement>(
      'input[aria-label="Hyphenation"]',
    );
    const rtl = container.querySelector<HTMLInputElement>('input[aria-label="Right to left"]');
    const writingMode = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Writing mode"]',
    );

    expect(brightness).not.toBeNull();
    expect(container.querySelector('input[aria-label="Zoom"]')).toBeNull();
    expect(hyphenation).not.toBeNull();
    expect(rtl).not.toBeNull();
    expect(writingMode).not.toBeNull();

    await act(async () => {
      if (!brightness || !hyphenation || !rtl || !writingMode) return;
      setInputValue(brightness, "112");
      hyphenation.click();
      rtl.click();
      writingMode.value = "vertical-rl";
      writingMode.dispatchEvent(new Event("change", { bubbles: true }));
      vi.advanceTimersByTime(450);
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({
        fontBrightness: 112,
        hyphenation: false,
        rtl: true,
        writingMode: "vertical-rl",
      }),
    );

    vi.useRealTimers();
  });

  it("keeps side panel tab labels visible in the narrow panel", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    const label = settingsTab?.querySelector("[data-reader-panel-tab-label]");

    expect(settingsTab?.className).toContain("flex-col");
    expect(label?.textContent).toBe("Settings");
    expect(label?.className).toContain("whitespace-normal");
  });

  it("keeps range labels readable in the settings panel", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    const brightness = container.querySelector<HTMLInputElement>('input[aria-label="Brightness"]');
    const label = brightness?.closest("label");
    const header = label?.querySelector("[data-reader-range-header]");
    const name = label?.querySelector("[data-reader-range-name]");
    const value = label?.querySelector("[data-reader-range-value]");

    expect(header?.className).toContain("grid");
    expect(name?.className).toContain("break-words");
    expect(value?.className).toContain("justify-self-end");
  });

  it("binds the reader keyboard map with an input guard", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const press = (key: string) => {
      act(() => {
        window.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
      });
    };

    // Escape precedence: shortcuts overlay, then the side panel, then chrome.
    press("?");
    expect(container.textContent).toContain("Keyboard shortcuts");
    expect(container.textContent).toContain("Previous / next page");
    expect(container.querySelector("aside")).not.toBeNull(); // panel open by default

    press("Escape"); // overlay open — Escape closes it first
    expect(container.textContent).not.toContain("Previous / next page");
    expect(container.querySelector("header")).not.toBeNull();
    expect(container.querySelector("aside")).not.toBeNull(); // panel untouched

    press("Escape"); // overlay closed, panel open — Escape closes the panel next
    expect(container.querySelector("aside")).toBeNull();
    expect(container.querySelector("header")).not.toBeNull(); // chrome untouched

    press("Escape"); // overlay closed, panel closed — Escape now toggles chrome
    expect(container.querySelector("header")).toBeNull();

    press("Escape"); // bring chrome back so the panel toggle button is reachable
    expect(container.querySelector("header")).not.toBeNull();

    const openPanel = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Open reader panel"]',
    );
    await act(async () => {
      openPanel?.click();
    });
    expect(container.querySelector("aside")).not.toBeNull();

    const closePanel = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Close reader panel"]',
    );
    await act(async () => {
      closePanel?.click();
    });
    expect(container.querySelector("aside")).toBeNull();

    // A keydown originating from an editable target (event.target is the
    // input, not window) must be ignored by the reader shortcut map.
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "t", bubbles: true }));
    });
    expect(container.querySelector("aside")).toBeNull();
    document.body.removeChild(input);

    // Outside of an editable target, "t" reopens the contents panel.
    press("t");
    expect(container.querySelector("aside")).not.toBeNull();
  });

  it("jumps to chapter bounds on Home and End using section fractions", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    act(() => {
      mocks.lastOnLocationChange?.({ fraction: 0.3, sectionIndex: 1, tocLabel: "Ch 2" });
    });

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Home" }));
    });
    expect(mocks.readerGoToFraction).toHaveBeenCalledWith(0.25);

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "End" }));
    });
    expect(mocks.readerGoToFraction).toHaveBeenCalledWith(0.6);
  });

  it("keeps Home and End inert for comic formats", async () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem({
        versions: [makeVersion({ file_id: 8, file_name: "Comic.cbz", container: "cbz" })],
      }),
      isLoading: false,
      error: null,
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Home" }));
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "End" }));
    });

    expect(mocks.readerGoToFraction).not.toHaveBeenCalled();
  });

  it("hides paginated-only controls in scrolled flow", async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/reader/ebook/ebook-1"]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });

    expect(container.querySelector('input[aria-label="Width"]')).not.toBeNull();

    const flow = container.querySelector<HTMLSelectElement>('select[aria-label="Flow"]');
    await act(async () => {
      if (!flow) return;
      flow.value = "scrolled";
      flow.dispatchEvent(new Event("change", { bubbles: true }));
    });

    expect(container.querySelector('input[aria-label="Width"]')).toBeNull();
  });
});
