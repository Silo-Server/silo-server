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
  readerGoTo: vi.fn(),
  readerGoToFraction: vi.fn(),
  readerSearch: vi.fn(),
  captureReaderSettings: vi.fn(),
  fetchEbookReaderConfig: vi.fn(),
  saveEbookReaderConfig: vi.fn(),
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
  createEbookReaderAnnotation: mocks.createEbookReaderAnnotation,
  deleteEbookReaderAnnotation: mocks.deleteEbookReaderAnnotation,
  fetchEbookReaderAnnotations: mocks.fetchEbookReaderAnnotations,
  fetchEbookReaderConfig: mocks.fetchEbookReaderConfig,
  saveEbookReaderConfig: mocks.saveEbookReaderConfig,
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
    mocks.readerPrev.mockReset();
    mocks.readerNext.mockReset();
    mocks.readerGoTo.mockReset();
    mocks.readerGoToFraction.mockReset();
    mocks.readerSearch.mockReset();
    mocks.captureReaderSettings.mockReset();
    mocks.fetchEbookReaderConfig.mockReset();
    mocks.saveEbookReaderConfig.mockReset();
    mocks.fetchEbookReaderAnnotations.mockReset();
    mocks.createEbookReaderAnnotation.mockReset();
    mocks.deleteEbookReaderAnnotation.mockReset();
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

    const theme = container.querySelector<HTMLSelectElement>('select[aria-label="Theme"]');
    await act(async () => {
      if (!theme) return;
      theme.value = "dark";
      theme.dispatchEvent(new Event("change", { bubbles: true }));
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

    const comfortable = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Apply comfortable reading profile"]',
    );
    await act(async () => {
      comfortable?.click();
    });

    expect(mocks.captureReaderSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({
        fontFamily: "ui-serif, Georgia, Cambria, \"Times New Roman\", Times, serif",
        fontSize: 112,
        lineHeight: 1.75,
      }),
    );
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
    expect(
      container.querySelector('[aria-label="Reading ruler - drag vertically to reposition"]'),
    ).not.toBeNull();
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

    const brightness = container.querySelector<HTMLInputElement>(
      'input[aria-label="Brightness"]',
    );
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

    const brightness = container.querySelector<HTMLInputElement>(
      'input[aria-label="Brightness"]',
    );
    const label = brightness?.closest("label");
    const header = label?.querySelector("[data-reader-range-header]");
    const name = label?.querySelector("[data-reader-range-name]");
    const value = label?.querySelector("[data-reader-range-value]");

    expect(header?.className).toContain("grid");
    expect(name?.className).toContain("break-words");
    expect(value?.className).toContain("justify-self-end");
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
    expect(container.querySelector('select[aria-label="Spread"]')).not.toBeNull();

    const flow = container.querySelector<HTMLSelectElement>('select[aria-label="Flow"]');
    await act(async () => {
      if (!flow) return;
      flow.value = "scrolled";
      flow.dispatchEvent(new Event("change", { bubbles: true }));
    });

    expect(container.querySelector('input[aria-label="Width"]')).toBeNull();
    expect(container.querySelector('select[aria-label="Spread"]')).toBeNull();
  });
});
