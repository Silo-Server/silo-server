// @vitest-environment jsdom

import { act, useEffect, useImperativeHandle, forwardRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FileVersion, ItemDetail } from "@/api/types";
import EbookReader from "./EbookReader";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
  readerPrev: vi.fn(),
  readerNext: vi.fn(),
  readerProgress: 0.421,
  readerGoTo: vi.fn(),
  readerGoToFraction: vi.fn(),
  readerSearch: vi.fn(),
  captureReaderSettings: vi.fn(),
  fetchEbookReaderConfig: vi.fn(),
  saveEbookReaderConfig: vi.fn(),
  saveEbookReaderConfigKeepalive: vi.fn(),
  fetchEbookReaderAnnotations: vi.fn(),
  createEbookReaderAnnotation: vi.fn(),
  deleteEbookReaderAnnotation: vi.fn(),
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: mocks.useCatalogItemDetail,
}));

vi.mock("@/components/PageBack", () => ({
  default: () => <div />,
}));

vi.mock("@/reader/ebookReaderApi", () => ({
  createEbookAnnotationSession: () => ({ profileContext: null, creates: new Map() }),
  createEbookReaderAnnotation: mocks.createEbookReaderAnnotation,
  deleteEbookReaderAnnotation: mocks.deleteEbookReaderAnnotation,
  fetchEbookReaderAnnotations: mocks.fetchEbookReaderAnnotations,
  createEbookReaderConfigSession: () => ({
    etag: "test",
    profileContext: null,
    pending: Promise.resolve(),
  }),
  fetchEbookReaderConfig: mocks.fetchEbookReaderConfig,
  saveEbookReaderConfig: mocks.saveEbookReaderConfig,
  saveEbookReaderConfigKeepalive: mocks.saveEbookReaderConfigKeepalive,
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
      },
      {
        file: FileVersion;
        settings?: unknown;
        annotations?: unknown[];
        onProgressChange?: (progress: number | null) => void;
        onFileLoaded?: (state: { objectUrl: string; filename: string } | null) => void;
        onSelectionChange?: (selection: { cfi: string; selectedText: string } | null) => void;
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
      { file, settings, onProgressChange, onFileLoaded, onSelectionChange, onReady },
      ref,
    ) {
      mocks.captureReaderSettings(settings);
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
      }));
      useEffect(() => {
        onFileLoaded?.({ objectUrl: "blob:ebook", filename: "Reader.epub" });
        onProgressChange?.(mocks.readerProgress);
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
    ratings: [],
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

function HistoryBackProbe() {
  const navigate = useNavigate();
  return (
    <button type="button" aria-label="Browser back" onClick={() => navigate(-1)}>
      Browser back
    </button>
  );
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
    for (const mock of Object.values(mocks)) if (typeof mock === "function") mock.mockReset();
    mocks.readerProgress = 0.421;
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
    localStorage.clear();
    mocks.useCatalogItemDetail.mockReturnValue({
      data: makeEbookItem(),
      isLoading: false,
      error: null,
    });
  });

  async function openReader(entry = "/reader/ebook/ebook-1") {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[entry]}>
          <Routes>
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });
  }

  // Picks a theme on the Reader settings tab.
  async function chooseTheme(value: string) {
    const settingsTab = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Reader settings"]',
    );
    await act(async () => {
      settingsTab?.click();
    });
    const theme = container.querySelector<HTMLSelectElement>('select[aria-label="Theme"]');
    await act(async () => {
      if (!theme) return;
      theme.value = value;
      theme.dispatchEvent(new Event("change", { bubbles: true }));
    });
  }

  afterEach(async () => {
    vi.useRealTimers();
    window.history.replaceState(null, "");
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  // Regression test for issue #189: exiting the reader must consume the
  // reader's history entry (history back) rather than pushing the series page
  // on top of it — otherwise pressing back on the series page re-opens the
  // reader. With in-app history present, clicking Back returns to the entry
  // the reader was opened from, not to a fresh push of the backTo target.
  it("goes back through history on Back instead of pushing the backTo target", async () => {
    window.history.replaceState({ idx: 1 }, "");
    const backTo = encodeURIComponent("/item/manga-series-1?libraryId=7");
    await act(async () => {
      root.render(
        <MemoryRouter
          initialEntries={["/came-from-here", `/reader/ebook/ebook-1?libraryId=7&backTo=${backTo}`]}
          initialIndex={1}
        >
          <Routes>
            <Route path="/came-from-here" element={<div data-testid="origin-page" />} />
            <Route path="/item/:contentId" element={<div data-testid="pushed-series-page" />} />
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const back = container.querySelector('a[aria-label="Back"], [aria-label="Back"]');
    expect(back).not.toBeNull();
    await act(async () => {
      back!.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(container.querySelector('[data-testid="origin-page"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="pushed-series-page"]')).toBeNull();
  });

  it("replaces a direct reader entry when falling back to the backTo target", async () => {
    window.history.replaceState({ idx: 0 }, "");
    const backTo = encodeURIComponent("/item/manga-series-1?libraryId=7");
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[`/reader/ebook/ebook-1?libraryId=7&backTo=${backTo}`]}>
          <Routes>
            <Route
              path="/item/:contentId"
              element={
                <div data-testid="series-page">
                  <HistoryBackProbe />
                </div>
              }
            />
            <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
          </Routes>
        </MemoryRouter>,
      );
    });

    expect(container.innerHTML).toContain('href="/item/manga-series-1?libraryId=7"');
    expect(container.innerHTML).not.toContain('href="/item/ebook-1?libraryId=7"');

    const back = container.querySelector<HTMLAnchorElement>('a[aria-label="Back"]');
    expect(back).not.toBeNull();
    await act(async () => {
      back?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(container.querySelector('[data-testid="series-page"]')).not.toBeNull();

    const browserBack = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Browser back"]',
    );
    await act(async () => {
      browserBack?.click();
    });

    expect(container.querySelector('[data-testid="series-page"]')).not.toBeNull();
    expect(container.textContent).not.toContain("reader surface");
  });

  it.each([["header", 0.421, 0, 1]] as const)(
    "replaces reader history when advancing with the %s Next control",
    async (_control, progress, linkIndex, expectedLinkCount) => {
      mocks.readerProgress = progress;
      mocks.useCatalogItemDetail.mockImplementation((requestedContentID?: string) => {
        if (requestedContentID === "manga-series-1") {
          return {
            data: {
              ...makeEbookItem(),
              content_id: "manga-series-1",
              type: "manga",
              title: "Manga Series",
              manga: {
                chapters: [
                  { content_id: "chapter-1", title: "Chapter 1", chapter_index: 1 },
                  { content_id: "chapter-2", title: "Chapter 2", chapter_index: 2 },
                ],
              },
            } as ItemDetail,
            isLoading: false,
            error: null,
          };
        }
        return {
          data: makeEbookItem({
            content_id: requestedContentID ?? "chapter-1",
            series_id: "manga-series-1",
          }),
          isLoading: false,
          error: null,
        };
      });
      window.history.replaceState({ idx: 1 }, "");
      const backTo = encodeURIComponent("/item/manga-series-1?libraryId=7");

      await act(async () => {
        root.render(
          <MemoryRouter
            initialEntries={[
              "/item/manga-series-1?libraryId=7",
              `/reader/ebook/chapter-1?libraryId=7&backTo=${backTo}`,
            ]}
            initialIndex={1}
          >
            <Routes>
              <Route path="/item/:contentId" element={<div data-testid="series-page" />} />
              <Route path="/reader/ebook/:contentId" element={<EbookReader />} />
            </Routes>
          </MemoryRouter>,
        );
      });

      const nextLinks = container.querySelectorAll<HTMLAnchorElement>(
        'a[href^="/reader/ebook/chapter-2"]',
      );
      expect(nextLinks).toHaveLength(expectedLinkCount);
      await act(async () => {
        nextLinks[linkIndex]?.dispatchEvent(
          new MouseEvent("click", { bubbles: true, cancelable: true }),
        );
      });

      const back = container.querySelector<HTMLAnchorElement>('a[aria-label="Back"]');
      expect(back).not.toBeNull();
      await act(async () => {
        back?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      });

      expect(container.querySelector('[data-testid="series-page"]')).not.toBeNull();
      expect(container.textContent).not.toContain("reader surface");
    },
  );

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

    await openReader("/reader/ebook/ebook-1?file_id=9");

    expect(container.textContent).toContain("reader surface Reader.epub");
    expect(container.textContent).not.toContain("Unsupported ebook format.");
  });

  it("persists reader settings to the server and local fallback", async () => {
    vi.useFakeTimers();
    mocks.fetchEbookReaderConfig.mockResolvedValue({
      settings: { theme: "sepia", fontSize: 130 },
    });

    await openReader();

    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.fetchEbookReaderConfig).toHaveBeenCalledWith("ebook-1", expect.any(Object));
    expect(mocks.captureReaderSettings).toHaveBeenCalledWith(
      expect.objectContaining({ theme: "sepia", fontSize: 130 }),
    );

    await chooseTheme("dark");

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
      expect.any(Object),
    );

    vi.useRealTimers();
  });

  it("flushes a pending debounced settings save when unmounting inside the debounce window", async () => {
    vi.useFakeTimers();

    await openReader();

    await chooseTheme("dark");
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
      expect.any(Object),
    );

    vi.useRealTimers();
  });

  it("flushes a pending debounced settings save with keepalive on pagehide, exactly once", async () => {
    vi.useFakeTimers();

    await openReader();

    await chooseTheme("sepia");

    await act(async () => {
      window.dispatchEvent(new Event("pagehide"));
    });

    expect(mocks.saveEbookReaderConfigKeepalive).toHaveBeenCalledTimes(1);
    expect(mocks.saveEbookReaderConfigKeepalive).toHaveBeenCalledWith(
      "ebook-1",
      expect.objectContaining({
        settings: expect.objectContaining({ theme: "sepia" }),
      }),
      expect.any(Object),
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

    await openReader();

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

    await openReader();

    await chooseTheme("dark");

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
      expect.any(Object),
    );
  });
});
