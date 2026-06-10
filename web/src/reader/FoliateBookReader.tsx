import { forwardRef, useCallback, useEffect, useImperativeHandle, useRef, useState } from "react";
import { type QueryClient, useQueryClient } from "@tanstack/react-query";

import { api, apiBlob } from "@/api/client";
import type { FileVersion } from "@/api/types";
import type { EbookReaderAnnotation } from "@/reader/ebookReaderApi";
import { DocumentLoader, type BookDoc, type TOCItem } from "@/reader/readest/libs/document";

type FoliateViewElement = HTMLElement & {
  open: (book: BookDoc) => Promise<void>;
  close?: () => void;
  init: (options: { lastLocation: string }) => Promise<void>;
  goToFraction: (fraction: number) => Promise<void>;
  goTo?: (href: string) => void;
  getCFI?: (index: number, range: Range) => string;
  addAnnotation?: (
    annotation: { value: string; color?: string; style?: string; note?: string },
    remove?: boolean,
  ) => void;
  deselect?: () => void;
  next?: () => void;
  prev?: () => void;
  search?: (
    options: ReaderSearchOptions & { query: string },
  ) => AsyncGenerator<FoliateSearchResult>;
  clearSearch?: () => void;
  renderer?: HTMLElement & {
    primaryIndex?: number;
    getContents?: () => Array<{ doc: Document; index?: number }>;
    setStyles?: (css: string) => void;
    render?: () => Promise<void>;
  };
};

export type ReaderLoadState = {
  objectUrl: string;
  filename: string;
};

export type EbookReaderProgressPayload = {
  file_id: number;
  location: string;
  progress: number;
};

export type EbookReaderProgress = EbookReaderProgressPayload & {
  content_id?: string;
  updated_at?: string;
};

export type RestoreProgressTarget =
  | { type: "location"; location: string }
  | { type: "fraction"; fraction: number };

export type FoliateBookReaderHandle = {
  next: () => void;
  prev: () => void;
  goTo: (href: string) => void;
  goToFraction: (fraction: number) => Promise<void>;
  search: (query: string) => Promise<ReaderSearchResult[]>;
  clearSearch: () => void;
  clearSelection: () => void;
  createSelectionAnnotation: () => ReaderSelection | null;
  getReadableText: () => string;
};

export type ReaderTheme = "light" | "sepia" | "dark";
export type ReaderFlow = "paginated" | "scrolled";
export type ReaderSpread = "auto" | "none";
export type ReaderWritingMode = "auto" | "horizontal-tb" | "vertical-rl";

export type ReaderSettings = {
  theme: ReaderTheme;
  fontFamily: string;
  fontSize: number;
  fontWeight: number;
  hyphenation: boolean;
  lineHeight: number;
  margin: number;
  maxWidth: number;
  spread: ReaderSpread;
  flow: ReaderFlow;
  fontBrightness: number;
  rtl: boolean;
  writingMode: ReaderWritingMode;
  readingRuler: boolean;
  readingRulerTop: number;
};

export type ReaderReadyState = {
  toc: TOCItem[];
};

export type ReaderSearchOptions = {
  matchCase?: boolean;
  matchDiacritics?: boolean;
  matchWholeWords?: boolean;
  scope?: "book" | "section";
};

export type ReaderSearchResult = {
  cfi: string;
  label?: string;
  excerpt?: string;
};

export type ReaderSelection = {
  cfi: string;
  rect: {
    height: number;
    left: number;
    top: number;
    width: number;
  };
  selectedText: string;
};

type FoliateSearchResult = {
  cfi?: string;
  excerpt?: string;
  label?: string;
  section?: { label?: string; href?: string };
  subitems?: Array<{
    cfi?: string;
    excerpt?: string;
    label?: string;
  }>;
};

type RelocateDetail = {
  cfi?: string;
  location?: {
    current?: number;
    total?: number;
  };
};

const READEST_FORMATS = new Set([
  "epub",
  "pdf",
  "mobi",
  "azw",
  "azw3",
  "cbz",
  "cbr",
  "fb2",
  "fbz",
  "md",
]);

export const READER_FONT_STACKS = {
  inherit: "inherit",
  serif: 'ui-serif, Georgia, Cambria, "Times New Roman", Times, serif',
  sans: 'ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
  mono: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
} as const;

// Font values persisted before the reader switched to generic stacks; without this
// mapping the font select renders blank for settings saved by older builds.
const LEGACY_READER_FONT_ALIASES: Record<string, string> = {
  "Inter, ui-sans-serif, system-ui, sans-serif": READER_FONT_STACKS.sans,
  "Georgia, serif": READER_FONT_STACKS.serif,
  "Merriweather, Georgia, serif": READER_FONT_STACKS.serif,
  "ui-serif, Georgia, Cambria, serif": READER_FONT_STACKS.serif,
};

export const DEFAULT_READER_SETTINGS: ReaderSettings = {
  theme: "light",
  fontFamily: READER_FONT_STACKS.inherit,
  fontSize: 112,
  fontWeight: 400,
  hyphenation: true,
  lineHeight: 1.65,
  margin: 24,
  maxWidth: 74,
  spread: "auto",
  flow: "paginated",
  fontBrightness: 100,
  rtl: false,
  writingMode: "auto",
  readingRuler: false,
  readingRulerTop: 50,
};

export function ebookReadPath(contentID: string, fileID: number): string {
  return `/ebooks/${encodeURIComponent(contentID)}/files/${fileID}/read`;
}

export function ebookProgressPath(contentID: string): string {
  return `/ebooks/${encodeURIComponent(contentID)}/progress`;
}

export function ebookReaderProgressQueryKey(contentID: string | undefined) {
  return ["ebook-reader-progress", contentID] as const;
}

export function cacheEbookReaderProgress(
  queryClient: QueryClient,
  contentID: string,
  progress: EbookReaderProgress,
) {
  queryClient.setQueryData(ebookReaderProgressQueryKey(contentID), progress);
}

export function readerFileFormat(file: FileVersion | undefined): string {
  if (!file) return "";
  const fileName = file.file_name || file.file_path || "";
  if (fileName.toLowerCase().endsWith(".fb2.zip")) return "fbz";
  const extension = /\.([a-z0-9]+)$/i.exec(fileName)?.[1]?.toLowerCase() ?? "";
  const container = file.container?.trim().toLowerCase();
  const normalizedContainer = container ? container.replace(/^\./, "") : "";
  if (normalizedContainer && normalizedContainer !== "zip" && normalizedContainer !== "rar") {
    return normalizedContainer;
  }
  return extension || normalizedContainer;
}

export function isReaderSupportedFile(file: FileVersion | undefined): boolean {
  return READEST_FORMATS.has(readerFileFormat(file));
}

export function readerMimeType(format: string): string {
  switch (format.toLowerCase()) {
    case "epub":
      return "application/epub+zip";
    case "pdf":
      return "application/pdf";
    case "mobi":
      return "application/x-mobipocket-ebook";
    case "azw":
      return "application/vnd.amazon.ebook";
    case "azw3":
      return "application/vnd.amazon.mobi8-ebook";
    case "cbz":
      return "application/vnd.comicbook+zip";
    case "cbr":
      return "application/vnd.comicbook-rar";
    case "fb2":
      return "application/x-fictionbook+xml";
    case "fbz":
      return "application/x-zip-compressed-fb2";
    case "md":
      return "text/markdown";
    default:
      return "application/octet-stream";
  }
}

export function progressFromRelocate(
  detail: RelocateDetail,
  fileID: number,
): EbookReaderProgressPayload | null {
  const current = detail.location?.current ?? 0;
  const total = detail.location?.total ?? 0;
  if (total <= 0 || current < 0) return null;
  const progress = Math.min(1, Math.max(0, (current + 1) / total));
  const cfi = typeof detail.cfi === "string" ? detail.cfi.trim() : "";
  return {
    file_id: fileID,
    location: cfi || `fraction:${progress.toFixed(6)}`,
    progress,
  };
}

export function restoreProgressTarget(
  progress: Pick<EbookReaderProgress, "file_id" | "location" | "progress"> | null | undefined,
): RestoreProgressTarget | null {
  if (!progress || typeof progress.location !== "string") return null;
  const location = progress.location.trim();
  if (!location) return null;
  if (location.startsWith("fraction:")) {
    const value = Number(location.slice("fraction:".length));
    const fraction = Number.isFinite(value) ? value : progress.progress;
    if (!Number.isFinite(fraction)) return null;
    return { type: "fraction", fraction: Math.min(1, Math.max(0, fraction)) };
  }
  return { type: "location", location };
}

export function formatReaderProgress(progress: number | null | undefined): string | null {
  if (typeof progress !== "number" || !Number.isFinite(progress)) return null;
  const bounded = Math.min(1, Math.max(0, progress));
  return `${Math.round(bounded * 100)}%`;
}

function clampNumber(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.min(max, Math.max(min, number));
}

function isReaderTheme(value: unknown): value is ReaderTheme {
  return value === "light" || value === "sepia" || value === "dark";
}

function isReaderFlow(value: unknown): value is ReaderFlow {
  return value === "paginated" || value === "scrolled";
}

function isReaderSpread(value: unknown): value is ReaderSpread {
  return value === "auto" || value === "none";
}

function isReaderWritingMode(value: unknown): value is ReaderWritingMode {
  return value === "auto" || value === "horizontal-tb" || value === "vertical-rl";
}

function normalizeReaderFontFamily(value: unknown): string {
  if (typeof value !== "string" || !value.trim()) return DEFAULT_READER_SETTINGS.fontFamily;
  const trimmed = value.trim();
  return LEGACY_READER_FONT_ALIASES[trimmed] ?? trimmed;
}

export function normalizeReaderSettings(settings?: Partial<ReaderSettings>): ReaderSettings {
  return {
    theme: isReaderTheme(settings?.theme) ? settings.theme : DEFAULT_READER_SETTINGS.theme,
    fontFamily: normalizeReaderFontFamily(settings?.fontFamily),
    fontSize: clampNumber(settings?.fontSize, DEFAULT_READER_SETTINGS.fontSize, 80, 180),
    fontWeight: clampNumber(settings?.fontWeight, DEFAULT_READER_SETTINGS.fontWeight, 300, 800),
    hyphenation:
      typeof settings?.hyphenation === "boolean"
        ? settings.hyphenation
        : DEFAULT_READER_SETTINGS.hyphenation,
    lineHeight: clampNumber(settings?.lineHeight, DEFAULT_READER_SETTINGS.lineHeight, 1.1, 2.4),
    margin: clampNumber(settings?.margin, DEFAULT_READER_SETTINGS.margin, 0, 64),
    maxWidth: clampNumber(settings?.maxWidth, DEFAULT_READER_SETTINGS.maxWidth, 42, 96),
    spread: isReaderSpread(settings?.spread) ? settings.spread : DEFAULT_READER_SETTINGS.spread,
    flow: isReaderFlow(settings?.flow) ? settings.flow : DEFAULT_READER_SETTINGS.flow,
    fontBrightness: clampNumber(
      settings?.fontBrightness,
      DEFAULT_READER_SETTINGS.fontBrightness,
      70,
      125,
    ),
    rtl: typeof settings?.rtl === "boolean" ? settings.rtl : DEFAULT_READER_SETTINGS.rtl,
    writingMode: isReaderWritingMode(settings?.writingMode)
      ? settings.writingMode
      : DEFAULT_READER_SETTINGS.writingMode,
    readingRuler:
      typeof settings?.readingRuler === "boolean"
        ? settings.readingRuler
        : DEFAULT_READER_SETTINGS.readingRuler,
    readingRulerTop: clampNumber(
      settings?.readingRulerTop,
      DEFAULT_READER_SETTINGS.readingRulerTop,
      0,
      100,
    ),
  };
}

export async function fetchEbookReaderProgress(
  contentID: string,
): Promise<EbookReaderProgress | null> {
  const progress = await api<Partial<EbookReaderProgress>>(ebookProgressPath(contentID));
  if (!progress || typeof progress.location !== "string" || progress.location.trim() === "") {
    return null;
  }
  if (typeof progress.file_id !== "number" || typeof progress.progress !== "number") {
    return null;
  }
  return {
    file_id: progress.file_id,
    location: progress.location,
    progress: progress.progress,
    content_id: progress.content_id,
    updated_at: progress.updated_at,
  };
}

export async function saveEbookReaderProgress(
  contentID: string,
  progress: EbookReaderProgressPayload,
): Promise<EbookReaderProgress> {
  return api<EbookReaderProgress>(ebookProgressPath(contentID), {
    method: "PUT",
    body: JSON.stringify(progress),
  });
}

function readerColors(theme: ReaderTheme) {
  switch (theme) {
    case "dark":
      return {
        background: "#111827",
        foreground: "#f8fafc",
        link: "#93c5fd",
        scheme: "dark",
      };
    case "sepia":
      return {
        background: "#f4ecd8",
        foreground: "#2f261b",
        link: "#8b5a2b",
        scheme: "light",
      };
    default:
      return {
        background: "#ffffff",
        foreground: "#171717",
        link: "#2563eb",
        scheme: "light",
      };
  }
}

export function readerStyles(settings: ReaderSettings = DEFAULT_READER_SETTINGS) {
  const colors = readerColors(settings.theme);
  const contentMaxWidth = settings.flow === "scrolled" ? "none" : `${settings.maxWidth}ch`;
  return `
    :root {
      --theme-bg-color: ${colors.background};
      --theme-fg-color: ${colors.foreground};
      --override-color: true;
      color-scheme: ${colors.scheme};
    }
    html, body {
      background: ${colors.background} !important;
      color: ${colors.foreground} !important;
      font-family: ${settings.fontFamily} !important;
      font-size: ${settings.fontSize}% !important;
      font-weight: ${settings.fontWeight} !important;
      hyphens: ${settings.hyphenation ? "auto" : "none"} !important;
      line-height: ${settings.lineHeight} !important;
      max-width: ${contentMaxWidth} !important;
      direction: ${settings.rtl ? "rtl" : "inherit"} !important;
      writing-mode: ${settings.writingMode === "auto" ? "inherit" : settings.writingMode} !important;
      filter: brightness(${settings.fontBrightness}%) !important;
    }
    body :where(p, span, div, li, blockquote, h1, h2, h3, h4, h5, h6,
                em, i, strong, b, code, pre, td, th, caption, figcaption,
                dt, dd, small, sub, sup, cite, q, mark) {
      color: ${colors.foreground} !important;
    }
    p, li, blockquote { margin-block: 0.75em !important; }
    a { color: ${colors.link} !important; }
  `;
}

export function readerRendererAttributes(settings: ReaderSettings) {
  const scrolled = settings.flow === "scrolled";
  const maxInlinePx = Math.round(settings.maxWidth * 10);
  return {
    flow: scrolled ? "scrolled" : null,
    gap: "7%",
    margin: `${settings.margin}px`,
    maxInlineSize: scrolled ? "9999px" : `${maxInlinePx}px`,
    maxColumnCount: scrolled ? "1" : settings.spread === "none" ? "1" : "2",
  };
}

function searchResultLabel(result: FoliateSearchResult, item?: { label?: string }): string {
  return item?.label || result.label || result.section?.label || "";
}

function flattenSearchResult(result: FoliateSearchResult): ReaderSearchResult[] {
  const direct = result.cfi
    ? [
        {
          cfi: result.cfi,
          label: searchResultLabel(result),
          excerpt: result.excerpt,
        },
      ]
    : [];
  const subitems = result.subitems ?? [];
  return direct.concat(
    subitems
      .filter((item) => typeof item.cfi === "string" && item.cfi.trim() !== "")
      .map((item) => ({
        cfi: item.cfi || "",
        label: searchResultLabel(result, item),
        excerpt: item.excerpt,
      })),
  );
}

type FoliateBookReaderProps = {
  contentID: string;
  file: FileVersion;
  title: string;
  annotations?: EbookReaderAnnotation[];
  settings?: Partial<ReaderSettings>;
  onFileLoaded?: (state: ReaderLoadState | null) => void;
  onProgressChange?: (progress: number | null) => void;
  onReady?: (state: ReaderReadyState) => void;
  onSelectionChange?: (selection: ReaderSelection | null) => void;
};

const FoliateBookReader = forwardRef<FoliateBookReaderHandle, FoliateBookReaderProps>(
  function FoliateBookReader(
    {
      contentID,
      file,
      title,
      annotations = [],
      settings,
      onFileLoaded,
      onProgressChange,
      onReady,
      onSelectionChange,
    },
    ref,
  ) {
    const queryClient = useQueryClient();
    const containerRef = useRef<HTMLDivElement>(null);
    const viewRef = useRef<FoliateViewElement | null>(null);
    const initializedRef = useRef(false);
    const saveTimerRef = useRef<number | null>(null);
    const pendingProgressRef = useRef<EbookReaderProgressPayload | null>(null);
    const settingsRef = useRef(normalizeReaderSettings(settings));
    const appliedRendererKeyRef = useRef("");
    const annotationsRef = useRef<EbookReaderAnnotation[]>(annotations);
    const drawnCfisRef = useRef<Set<string>>(new Set());
    const selectionCleanupRef = useRef<(() => void)[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState("");

    const applyReaderSettings = useCallback((nextSettings: Partial<ReaderSettings> | undefined) => {
      const normalized = normalizeReaderSettings(nextSettings);
      settingsRef.current = normalized;
      const renderer = viewRef.current?.renderer;
      if (!renderer) return;
      const styles = readerStyles(normalized);
      const attributes = readerRendererAttributes(normalized);
      // Settings such as the reading ruler never reach the renderer; re-styling and
      // re-rendering the book for those (or for any no-op update) causes visible jank.
      const rendererKey = JSON.stringify([styles, attributes]);
      if (rendererKey === appliedRendererKeyRef.current) return;
      appliedRendererKeyRef.current = rendererKey;
      renderer.setStyles?.(styles);
      renderer.setAttribute("gap", attributes.gap);
      renderer.setAttribute("margin", attributes.margin);
      renderer.setAttribute("max-inline-size", attributes.maxInlineSize);
      renderer.setAttribute("max-column-count", attributes.maxColumnCount);
      if (attributes.flow) {
        renderer.setAttribute("flow", attributes.flow);
      } else {
        renderer.removeAttribute("flow");
      }
      void renderer.render?.();
    }, []);

    const drawAnnotations = useCallback(() => {
      const view = viewRef.current;
      if (!view?.addAnnotation) return;
      const activeCfis = new Set(
        annotationsRef.current
          .filter((annotation) => annotation.kind !== "bookmark" && annotation.cfi_range)
          .map((annotation) => annotation.cfi_range || ""),
      );
      for (const cfi of drawnCfisRef.current) {
        if (!activeCfis.has(cfi)) {
          view.addAnnotation({ value: cfi }, true);
          drawnCfisRef.current.delete(cfi);
        }
      }
      for (const annotation of annotationsRef.current) {
        if (annotation.kind === "bookmark" || !annotation.cfi_range) continue;
        if (drawnCfisRef.current.has(annotation.cfi_range)) continue;
        view.addAnnotation({
          value: annotation.cfi_range,
          color: annotation.color || "#facc15",
          style: annotation.style || "highlight",
          note: annotation.note || undefined,
        });
        drawnCfisRef.current.add(annotation.cfi_range);
      }
    }, []);

    const createSelectionAnnotation = useCallback((): ReaderSelection | null => {
      const view = viewRef.current;
      const contents = view?.renderer?.getContents?.() ?? [];
      if (!view?.getCFI) return null;
      for (const content of contents) {
        const selection = content.doc.getSelection();
        if (!selection || selection.isCollapsed || selection.rangeCount === 0) continue;
        const selectedText = selection.toString().trim();
        if (!selectedText) continue;
        const range = selection.getRangeAt(0);
        const cfi = view.getCFI(content.index ?? 0, range);
        const rangeRect = range.getBoundingClientRect();
        const frameRect = content.doc.defaultView?.frameElement?.getBoundingClientRect();
        return {
          cfi,
          selectedText,
          rect: {
            height: rangeRect.height,
            left: rangeRect.left + (frameRect?.left ?? 0),
            top: rangeRect.top + (frameRect?.top ?? 0),
            width: rangeRect.width,
          },
        };
      }
      return null;
    }, []);

    const emitSelectionChange = useCallback(() => {
      onSelectionChange?.(createSelectionAnnotation());
    }, [createSelectionAnnotation, onSelectionChange]);

    const attachSelectionListeners = useCallback(() => {
      for (const cleanup of selectionCleanupRef.current) cleanup();
      selectionCleanupRef.current = [];
      const contents = viewRef.current?.renderer?.getContents?.() ?? [];
      for (const content of contents) {
        const doc = content.doc;
        const handler = () => window.setTimeout(emitSelectionChange, 0);
        doc.addEventListener("selectionchange", handler);
        doc.addEventListener("pointerup", handler);
        doc.addEventListener("keyup", handler);
        selectionCleanupRef.current.push(() => {
          doc.removeEventListener("selectionchange", handler);
          doc.removeEventListener("pointerup", handler);
          doc.removeEventListener("keyup", handler);
        });
      }
    }, [emitSelectionChange]);

    const getReadableText = useCallback(() => {
      const contents = viewRef.current?.renderer?.getContents?.() ?? [];
      for (const content of contents) {
        const selectedText = content.doc.getSelection()?.toString().trim();
        if (selectedText) return selectedText;
      }
      const primaryIndex = viewRef.current?.renderer?.primaryIndex;
      const primary = contents.find((content) => content.index === primaryIndex) ?? contents[0];
      return (primary?.doc.body?.innerText ?? "").replace(/\s+/g, " ").trim().slice(0, 5000);
    }, []);

    useImperativeHandle(
      ref,
      () => ({
        next: () => viewRef.current?.next?.(),
        prev: () => viewRef.current?.prev?.(),
        goTo: (href: string) => viewRef.current?.goTo?.(href),
        goToFraction: async (fraction: number) => {
          await viewRef.current?.goToFraction(Math.min(1, Math.max(0, fraction)));
        },
        search: async (query: string) => {
          const trimmed = query.trim();
          const view = viewRef.current;
          if (!trimmed || !view?.search) return [];
          const results: ReaderSearchResult[] = [];
          for await (const result of view.search({ query: trimmed, scope: "book" })) {
            results.push(...flattenSearchResult(result));
          }
          return results;
        },
        clearSearch: () => viewRef.current?.clearSearch?.(),
        clearSelection: () => {
          viewRef.current?.deselect?.();
          onSelectionChange?.(null);
        },
        createSelectionAnnotation,
        getReadableText,
      }),
      [createSelectionAnnotation, getReadableText, onSelectionChange],
    );

    useEffect(() => {
      applyReaderSettings(settings);
    }, [applyReaderSettings, settings]);

    useEffect(() => {
      annotationsRef.current = annotations;
      drawAnnotations();
    }, [annotations, drawAnnotations]);

    useEffect(() => {
      let cancelled = false;
      let objectUrl: string | null = null;
      setLoading(true);
      setError("");
      onFileLoaded?.(null);
      onProgressChange?.(null);

      const flushProgress = () => {
        const pending = pendingProgressRef.current;
        if (!pending) return;
        pendingProgressRef.current = null;
        void saveEbookReaderProgress(contentID, pending).then((saved) => {
          cacheEbookReaderProgress(queryClient, contentID, saved);
        });
      };

      const scheduleProgressSave = (progress: EbookReaderProgressPayload) => {
        pendingProgressRef.current = progress;
        if (saveTimerRef.current !== null) {
          window.clearTimeout(saveTimerRef.current);
        }
        saveTimerRef.current = window.setTimeout(() => {
          saveTimerRef.current = null;
          flushProgress();
        }, 800);
      };

      async function open() {
        try {
          const format = readerFileFormat(file);
          const [blob, savedProgress] = await Promise.all([
            apiBlob(ebookReadPath(contentID, file.file_id)),
            fetchEbookReaderProgress(contentID),
          ]);
          if (cancelled) return;

          objectUrl = URL.createObjectURL(blob);
          const filename = file.file_name || `${title}.${format || "ebook"}`;
          onFileLoaded?.({ objectUrl, filename });
          const documentFile = new File([blob], filename, {
            type: blob.type || readerMimeType(format),
          });
          const { book } = await new DocumentLoader(documentFile).open();
          if (cancelled) return;

          await import("foliate-js/view.js");
          const view = document.createElement("foliate-view") as FoliateViewElement;
          viewRef.current = view;
          containerRef.current?.replaceChildren(view);
          view.addEventListener("draw-annotation", async (event: Event) => {
            const { Overlayer } = await import("foliate-js/overlayer.js");
            const detail = (
              event as CustomEvent<{
                annotation: { color?: string; style?: string };
                draw: (fn: unknown, options?: Record<string, unknown>) => void;
              }>
            ).detail;
            const style = detail.annotation.style || "highlight";
            const color = detail.annotation.color || "#facc15";
            const draw =
              style === "underline"
                ? Overlayer.underline
                : style === "squiggly"
                  ? Overlayer.squiggly
                  : Overlayer.highlight;
            detail.draw(draw, { color });
          });
          view.addEventListener("create-overlay", () => {
            attachSelectionListeners();
            drawAnnotations();
          });
          view.addEventListener("relocate", (event: Event) => {
            if (!initializedRef.current) return;
            const progress = progressFromRelocate(
              (event as CustomEvent<RelocateDetail>).detail,
              file.file_id,
            );
            if (progress) {
              onProgressChange?.(progress.progress);
              scheduleProgressSave(progress);
            }
          });
          await view.open(book);
          onReady?.({ toc: book.toc ?? [] });
          applyReaderSettings(settingsRef.current);
          attachSelectionListeners();
          drawAnnotations();
          const savedFileProgress = savedProgress?.file_id === file.file_id ? savedProgress : null;
          const restoreTarget = restoreProgressTarget(savedFileProgress);
          if (savedFileProgress && restoreTarget?.type === "location") {
            onProgressChange?.(savedFileProgress.progress);
            await view.init({ lastLocation: restoreTarget.location });
          } else if (savedFileProgress && restoreTarget?.type === "fraction") {
            onProgressChange?.(savedFileProgress.progress);
            await view.goToFraction(restoreTarget.fraction);
          } else {
            await view.goToFraction(0);
          }
          initializedRef.current = true;
          setLoading(false);
        } catch (err) {
          if (!cancelled) {
            setError(err instanceof Error ? err.message : "Unable to open ebook");
            setLoading(false);
          }
        }
      }

      void open();
      const drawnCfis = drawnCfisRef.current;
      return () => {
        cancelled = true;
        initializedRef.current = false;
        if (saveTimerRef.current !== null) {
          window.clearTimeout(saveTimerRef.current);
          saveTimerRef.current = null;
        }
        flushProgress();
        viewRef.current?.close?.();
        viewRef.current?.remove();
        viewRef.current = null;
        appliedRendererKeyRef.current = "";
        for (const cleanup of selectionCleanupRef.current) cleanup();
        selectionCleanupRef.current = [];
        drawnCfis.clear();
        if (objectUrl) URL.revokeObjectURL(objectUrl);
        onFileLoaded?.(null);
        onProgressChange?.(null);
      };
    }, [
      applyReaderSettings,
      attachSelectionListeners,
      contentID,
      drawAnnotations,
      file,
      onFileLoaded,
      onProgressChange,
      onReady,
      queryClient,
      title,
    ]);

    return (
      <div className="relative h-full w-full overflow-hidden bg-white text-neutral-950">
        <div ref={containerRef} className="h-full w-full" />
        {loading && !error && (
          <div className="absolute inset-0 flex items-center justify-center bg-white text-sm text-neutral-500">
            Loading reader...
          </div>
        )}
        {error && (
          <div className="absolute inset-0 flex items-center justify-center bg-white p-6 text-center text-sm text-red-600">
            {error}
          </div>
        )}
      </div>
    );
  },
);

export default FoliateBookReader;
