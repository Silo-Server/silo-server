import { forwardRef, useCallback, useEffect, useImperativeHandle, useRef, useState } from "react";
import { type QueryClient, useQueryClient } from "@tanstack/react-query";

import { api, apiBlob, apiKeepalive } from "@/api/client";
import type { FileVersion } from "@/api/types";
import { ebookKeys } from "@/hooks/queries/keys";
import type { EbookReaderAnnotation } from "@/reader/ebookReaderApi";
import { DocumentLoader, type BookDoc, type TOCItem } from "@/reader/readest/libs/document";
import {
  legacyThemeFor,
  READER_THEMES,
  readerPalette,
  themeFromLegacy,
  type ReaderThemeName,
  type ReaderThemeVariant,
} from "@/reader/readerThemes";

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
  getSectionFractions?: () => number[];
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
  getSectionFractions: () => number[];
  // Synchronous read of whether any content doc currently holds a
  // non-collapsed selection. Unlike onSelectionChange (which the caller
  // learns about through a state update that FoliateBookReader itself
  // defers with setTimeout(0) to let native selection settle), this reads
  // the live DOM selection directly, so it is safe to consult from a
  // pointerup handler that must decide same-tick whether a tap ending a
  // selection should also page-turn.
  hasLiveSelection: () => boolean;
};

export type ReaderTheme = "light" | "sepia" | "dark";
export type ReaderFlow = "paginated" | "scrolled";
export type ReaderColumns = "auto" | 1 | 2 | 3 | 4;
export type ReaderWritingMode = "auto" | "horizontal-tb" | "vertical-rl";

export type ReaderSettings = {
  // Legacy tri-state theme, kept in sync with themeName/themeVariant on every
  // normalize so settings persisted by older clients keep rendering correctly.
  theme: ReaderTheme;
  themeName: ReaderThemeName;
  themeVariant: ReaderThemeVariant;
  fontFamily: string;
  fontSize: number;
  fontWeight: number;
  hyphenation: boolean;
  lineHeight: number;
  margin: number;
  maxWidth: number;
  columns: ReaderColumns;
  columnGap: number;
  justify: boolean;
  customFontID: number | null;
  flow: ReaderFlow;
  fontBrightness: number;
  rtl: boolean;
  writingMode: ReaderWritingMode;
  readingRuler: boolean;
  readingRulerTop: number;
};

// normalizeReaderSettings sanitizes both freshly typed updates and untyped
// input (parsed JSON from localStorage/server config, which may still carry
// fields from older settings shapes, e.g. the removed `spread`). Every known
// field is accepted as `unknown` so per-field guards below decide validity.
type ReaderSettingsInput = Partial<Record<keyof ReaderSettings, unknown>> & {
  spread?: unknown;
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
  fraction?: number;
  index?: number;
  tocItem?: { label?: string };
  location?: {
    current?: number;
    total?: number;
  };
};

export type ReaderLocationInfo = {
  fraction: number;
  sectionIndex: number | null;
  tocLabel: string | null;
};

// foliate book documents expose destroy() to release cached resource blob URLs,
// but the readest BookDoc interface does not declare it.
type DisposableBookDoc = BookDoc & { destroy?: () => void };

const READEST_FORMATS = new Set(["epub", "pdf", "mobi", "azw", "azw3", "cbz", "cbr", "fb2", "fbz"]);

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
  themeName: "default",
  themeVariant: "light",
  fontFamily: READER_FONT_STACKS.inherit,
  fontSize: 112,
  fontWeight: 400,
  hyphenation: true,
  lineHeight: 1.65,
  margin: 24,
  maxWidth: 74,
  columns: "auto",
  columnGap: 7,
  justify: false,
  customFontID: null,
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
  return ebookKeys.readerProgress(contentID);
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

function clampFraction(value: number): number {
  return Math.min(1, Math.max(0, value));
}

/**
 * Parses a stored reader location into a navigation target. Locations are either
 * raw CFI/href strings or synthetic `fraction:<n>` values (used for bookmarks and
 * progress in formats without CFIs), which foliate cannot resolve directly.
 */
export function parseReaderLocation(
  location: string | null | undefined,
): RestoreProgressTarget | null {
  if (typeof location !== "string") return null;
  const trimmed = location.trim();
  if (!trimmed) return null;
  if (trimmed.startsWith("fraction:")) {
    const value = Number(trimmed.slice("fraction:".length));
    if (!Number.isFinite(value)) return null;
    return { type: "fraction", fraction: clampFraction(value) };
  }
  return { type: "location", location: trimmed };
}

export function restoreProgressTarget(
  progress: Pick<EbookReaderProgress, "file_id" | "location" | "progress"> | null | undefined,
): RestoreProgressTarget | null {
  if (!progress || typeof progress.location !== "string") return null;
  const location = progress.location.trim();
  const target = parseReaderLocation(location);
  if (target) return target;
  // A malformed fraction location can still be restored from the stored progress value.
  if (location.startsWith("fraction:") && Number.isFinite(progress.progress)) {
    return { type: "fraction", fraction: clampFraction(progress.progress) };
  }
  return null;
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

function isReaderWritingMode(value: unknown): value is ReaderWritingMode {
  return value === "auto" || value === "horizontal-tb" || value === "vertical-rl";
}

function isReaderThemeName(value: unknown): value is ReaderThemeName {
  return typeof value === "string" && value in READER_THEMES;
}

function isReaderThemeVariant(value: unknown): value is ReaderThemeVariant {
  return value === "light" || value === "dark";
}

function normalizeReaderFontFamily(value: unknown): string {
  if (typeof value !== "string" || !value.trim()) return DEFAULT_READER_SETTINGS.fontFamily;
  const trimmed = value.trim();
  return LEGACY_READER_FONT_ALIASES[trimmed] ?? trimmed;
}

export function normalizeReaderSettings(settings?: ReaderSettingsInput): ReaderSettings {
  const raw = settings ?? {};

  // The theme-pair fields win when present; otherwise fall back to whatever
  // the legacy tri-state `theme` value (or its own default) maps to, so
  // settings saved before the palette-pair model existed still resolve.
  const explicitName = isReaderThemeName(raw.themeName) ? raw.themeName : null;
  const explicitVariant = isReaderThemeVariant(raw.themeVariant) ? raw.themeVariant : null;
  const legacyTheme = isReaderTheme(raw.theme) ? raw.theme : DEFAULT_READER_SETTINGS.theme;
  const fromLegacy = themeFromLegacy(legacyTheme);
  const themeName = explicitName ?? fromLegacy.themeName;
  const themeVariant = explicitVariant ?? fromLegacy.themeVariant;

  // `spread: "none"` was the pre-columns way to request single-page mode;
  // migrate it to columns only when the caller didn't already send columns.
  const columns: ReaderColumns =
    raw.columns === 1 || raw.columns === 2 || raw.columns === 3 || raw.columns === 4
      ? raw.columns
      : raw.spread === "none"
        ? 1
        : DEFAULT_READER_SETTINGS.columns;

  const fontFamily = normalizeReaderFontFamily(raw.fontFamily);
  const customFontID =
    typeof raw.customFontID === "number" &&
    Number.isInteger(raw.customFontID) &&
    raw.customFontID > 0
      ? raw.customFontID
      : null;

  return {
    theme: legacyThemeFor(themeName, themeVariant),
    themeName,
    themeVariant,
    // "custom" only means anything paired with a customFontID; without one
    // (e.g. hand-edited or partially-migrated persisted settings) there's no
    // font to render, so fall back to the book's own typeface rather than
    // rendering an unstyled "silo-custom-font" with no @font-face rule.
    fontFamily:
      fontFamily === "custom" && customFontID == null ? READER_FONT_STACKS.inherit : fontFamily,
    fontSize: clampNumber(raw.fontSize, DEFAULT_READER_SETTINGS.fontSize, 80, 180),
    fontWeight: clampNumber(raw.fontWeight, DEFAULT_READER_SETTINGS.fontWeight, 300, 800),
    hyphenation:
      typeof raw.hyphenation === "boolean" ? raw.hyphenation : DEFAULT_READER_SETTINGS.hyphenation,
    lineHeight: clampNumber(raw.lineHeight, DEFAULT_READER_SETTINGS.lineHeight, 1.1, 2.4),
    margin: clampNumber(raw.margin, DEFAULT_READER_SETTINGS.margin, 0, 64),
    maxWidth: clampNumber(raw.maxWidth, DEFAULT_READER_SETTINGS.maxWidth, 42, 96),
    columns,
    columnGap: clampNumber(raw.columnGap, DEFAULT_READER_SETTINGS.columnGap, 0, 50),
    justify: raw.justify === true,
    customFontID,
    flow: isReaderFlow(raw.flow) ? raw.flow : DEFAULT_READER_SETTINGS.flow,
    fontBrightness: clampNumber(
      raw.fontBrightness,
      DEFAULT_READER_SETTINGS.fontBrightness,
      70,
      125,
    ),
    rtl: typeof raw.rtl === "boolean" ? raw.rtl : DEFAULT_READER_SETTINGS.rtl,
    writingMode: isReaderWritingMode(raw.writingMode)
      ? raw.writingMode
      : DEFAULT_READER_SETTINGS.writingMode,
    readingRuler:
      typeof raw.readingRuler === "boolean"
        ? raw.readingRuler
        : DEFAULT_READER_SETTINGS.readingRuler,
    readingRulerTop: clampNumber(
      raw.readingRulerTop,
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

const CUSTOM_FONT_FAMILY = "silo-custom-font";

/**
 * Builds the CSS injected into foliate's srcdoc iframes. `customFontUrl` must
 * be an already-authenticated `blob:` URL (see `fetchReaderFontObjectUrl` in
 * `readerFontsApi.ts`) rather than the raw `/api/v1/...` font file path: the
 * iframe's CSS engine fetches `@font-face` src URLs itself and cannot attach
 * the bearer token or X-Profile-Id header the reader-fonts endpoint requires,
 * so an API path here would 401 silently and the font would never render.
 * When the url hasn't loaded yet (or failed to), the @font-face rule and the
 * "silo-custom-font" family are both omitted so the book falls back to its
 * own typeface instead of an unstyled custom font.
 */
export function readerStyles(
  settings: ReaderSettings = DEFAULT_READER_SETTINGS,
  customFontUrl?: string | null,
) {
  const colors = readerPalette(settings.themeName, settings.themeVariant);
  const contentMaxWidth = settings.flow === "scrolled" ? "none" : `${settings.maxWidth}ch`;
  const fontFamily =
    settings.fontFamily === "custom" && customFontUrl
      ? `"${CUSTOM_FONT_FAMILY}"`
      : settings.fontFamily;
  const fontFace = customFontUrl
    ? `@font-face { font-family: "${CUSTOM_FONT_FAMILY}"; src: url("${customFontUrl}"); font-display: swap; }`
    : "";
  return `
    ${fontFace}
    :root {
      --theme-bg-color: ${colors.background};
      --theme-fg-color: ${colors.foreground};
      --override-color: true;
      color-scheme: ${colors.scheme};
    }
    html, body {
      background: ${colors.background} !important;
      color: ${colors.foreground} !important;
      font-family: ${fontFamily} !important;
      font-size: ${settings.fontSize}% !important;
      font-weight: ${settings.fontWeight} !important;
      hyphens: ${settings.hyphenation ? "auto" : "none"} !important;
      line-height: ${settings.lineHeight} !important;
      max-width: ${contentMaxWidth} !important;
      text-align: ${settings.justify ? "justify" : "initial"} !important;
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
    gap: `${settings.columnGap}%`,
    margin: `${settings.margin}px`,
    maxInlineSize: scrolled ? "9999px" : `${maxInlinePx}px`,
    maxColumnCount: scrolled ? "1" : settings.columns === "auto" ? "2" : String(settings.columns),
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

export type ReaderContentPoint = { clientX: number; clientY: number };
export type ReaderContentVerticalPoint = { clientY: number };

type FoliateBookReaderProps = {
  contentID: string;
  file: FileVersion;
  title: string;
  annotations?: EbookReaderAnnotation[];
  settings?: Partial<ReaderSettings>;
  // Authenticated blob: URL for settings.customFontID's file, or null while
  // it's loading/failed to load. See readerStyles' doc comment for why this
  // can't just be derived from customFontID inside this component.
  customFontUrl?: string | null;
  onFileLoaded?: (state: ReaderLoadState | null) => void;
  onProgressChange?: (progress: number | null) => void;
  onLocationChange?: (info: ReaderLocationInfo) => void;
  onReady?: (state: ReaderReadyState) => void;
  onSelectionChange?: (selection: ReaderSelection | null) => void;
  // foliate renders prose content inside a same-origin iframe that fills the
  // reading surface, so a page's own pointerup/mousemove listeners never see
  // taps or hovers over the book text (iframe events do not bubble to the
  // parent document). These callbacks bridge that boundary: they fire with
  // coordinates already translated into the outer viewport's space so
  // callers can treat them like any other page-level pointer event.
  onContentPointerUp?: (point: ReaderContentPoint) => void;
  onContentPointerMove?: (point: ReaderContentVerticalPoint) => void;
};

const FoliateBookReader = forwardRef<FoliateBookReaderHandle, FoliateBookReaderProps>(
  function FoliateBookReader(
    {
      contentID,
      file,
      title,
      annotations = [],
      settings,
      customFontUrl,
      onFileLoaded,
      onProgressChange,
      onLocationChange,
      onReady,
      onSelectionChange,
      onContentPointerUp,
      onContentPointerMove,
    },
    ref,
  ) {
    const queryClient = useQueryClient();
    const containerRef = useRef<HTMLDivElement>(null);
    const viewRef = useRef<FoliateViewElement | null>(null);
    const initializedRef = useRef(false);
    const saveTimerRef = useRef<number | null>(null);
    const pendingProgressRef = useRef<EbookReaderProgressPayload | null>(null);
    const progressSaveSeqRef = useRef(0);
    const settingsRef = useRef(normalizeReaderSettings(settings));
    const appliedRendererKeyRef = useRef("");
    const annotationsRef = useRef<EbookReaderAnnotation[]>(annotations);
    const drawnCfisRef = useRef<Set<string>>(new Set());
    const selectionCleanupRef = useRef<(() => void)[]>([]);
    // Synchronous mirror of "is there a live, non-collapsed selection",
    // updated directly inside the selection listeners below (before their
    // setTimeout(0) deferral) so hasLiveSelection() never reports stale data
    // to a same-tick caller. See the FoliateBookReaderHandle doc comment.
    const hasLiveSelectionRef = useRef(false);
    // Held in refs (updated every render, read from listeners) rather than
    // effect dependencies so the content-doc listeners below never need to be
    // torn down and re-attached just because a caller passed a new inline
    // callback identity.
    const onContentPointerUpRef = useRef(onContentPointerUp);
    onContentPointerUpRef.current = onContentPointerUp;
    const onContentPointerMoveRef = useRef(onContentPointerMove);
    onContentPointerMoveRef.current = onContentPointerMove;
    // Same ref-mirroring pattern: applyReaderSettings reads the latest font
    // url without needing to be redeclared (and re-diffed against the
    // renderer) on every render just because a caller passed a new prop
    // identity.
    const customFontUrlRef = useRef<string | null | undefined>(customFontUrl);
    customFontUrlRef.current = customFontUrl;
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState("");

    const applyReaderSettings = useCallback((nextSettings: Partial<ReaderSettings> | undefined) => {
      const normalized = normalizeReaderSettings(nextSettings);
      settingsRef.current = normalized;
      const renderer = viewRef.current?.renderer;
      if (!renderer) return;
      const styles = readerStyles(normalized, customFontUrlRef.current);
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

    // Mirrors the collapsed/empty-text checks createSelectionAnnotation uses
    // below, but only answers "is there a live selection" — no CFI/rect work
    // — so it's cheap enough to call from every selection-related listener.
    const computeHasLiveSelection = useCallback((): boolean => {
      const contents = viewRef.current?.renderer?.getContents?.() ?? [];
      for (const content of contents) {
        const selection = content.doc.getSelection();
        if (!selection || selection.isCollapsed || selection.rangeCount === 0) continue;
        if (selection.toString().trim()) return true;
      }
      return false;
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

    // Wires listeners directly onto each content doc (one per rendered
    // section/page, foliate re-creates them on "create-overlay"). This is the
    // only reliable way to observe interaction with the book text: foliate
    // renders it inside a same-origin iframe that fills the reading surface,
    // and iframe-internal events never bubble out to the host page.
    const attachSelectionListeners = useCallback(() => {
      for (const cleanup of selectionCleanupRef.current) cleanup();
      selectionCleanupRef.current = [];
      // Content docs are being (re)attached, so any previously live selection
      // no longer applies.
      hasLiveSelectionRef.current = false;
      const contents = viewRef.current?.renderer?.getContents?.() ?? [];
      for (const content of contents) {
        const doc = content.doc;
        const handler = () => {
          // Synchronous, ahead of the deferred emitSelectionChange below, so
          // a same-tick pointerup handler (e.g. EbookReader's tap dispatch)
          // reading hasLiveSelection() never sees a stale value.
          hasLiveSelectionRef.current = computeHasLiveSelection();
          window.setTimeout(emitSelectionChange, 0);
        };
        doc.addEventListener("selectionchange", handler);
        doc.addEventListener("pointerup", handler);
        doc.addEventListener("keyup", handler);
        // Translate the iframe-local coordinates into outer viewport
        // coordinates by adding the content iframe's own frame offset, the
        // same technique createSelectionAnnotation uses above for selection
        // rects.
        const contentPointerUp = (event: PointerEvent) => {
          const frameRect = doc.defaultView?.frameElement?.getBoundingClientRect();
          onContentPointerUpRef.current?.({
            clientX: event.clientX + (frameRect?.left ?? 0),
            clientY: event.clientY + (frameRect?.top ?? 0),
          });
        };
        const contentPointerMove = (event: MouseEvent) => {
          const frameRect = doc.defaultView?.frameElement?.getBoundingClientRect();
          onContentPointerMoveRef.current?.({
            clientY: event.clientY + (frameRect?.top ?? 0),
          });
        };
        doc.addEventListener("pointerup", contentPointerUp);
        doc.addEventListener("mousemove", contentPointerMove);
        selectionCleanupRef.current.push(() => {
          doc.removeEventListener("selectionchange", handler);
          doc.removeEventListener("pointerup", handler);
          doc.removeEventListener("keyup", handler);
          doc.removeEventListener("pointerup", contentPointerUp);
          doc.removeEventListener("mousemove", contentPointerMove);
        });
      }
    }, [computeHasLiveSelection, emitSelectionChange]);

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
          hasLiveSelectionRef.current = false;
          onSelectionChange?.(null);
        },
        createSelectionAnnotation,
        getReadableText,
        getSectionFractions: () => viewRef.current?.getSectionFractions?.() ?? [],
        hasLiveSelection: () => hasLiveSelectionRef.current,
      }),
      [createSelectionAnnotation, getReadableText, onSelectionChange],
    );

    useEffect(() => {
      applyReaderSettings(settings);
      // customFontUrl is not part of `settings`, but changing it (font
      // finishes loading, is replaced, or fails) still needs a re-style.
    }, [applyReaderSettings, settings, customFontUrl]);

    useEffect(() => {
      annotationsRef.current = annotations;
      drawAnnotations();
    }, [annotations, drawAnnotations]);

    useEffect(() => {
      let cancelled = false;
      let objectUrl: string | null = null;
      let openedBook: DisposableBookDoc | null = null;
      let openedView: FoliateViewElement | null = null;
      setLoading(true);
      setError("");
      onFileLoaded?.(null);
      onProgressChange?.(null);

      // Releases everything this effect run created. It runs from the effect
      // cleanup and again from late continuations of a superseded open(); it is
      // idempotent and never touches resources owned by a newer run.
      const disposeOpenArtifacts = () => {
        if (openedView) {
          openedView.close?.();
          openedView.remove();
          if (viewRef.current === openedView) {
            viewRef.current = null;
          }
          openedView = null;
        }
        if (openedBook) {
          openedBook.destroy?.();
          openedBook = null;
        }
        if (objectUrl) {
          URL.revokeObjectURL(objectUrl);
          objectUrl = null;
        }
      };

      // Progress is deliberately per-content (one record per book): opening another
      // format overwrites this position, keeping "your place in the book" shared
      // across formats.
      const flushProgress = (options?: { keepalive?: boolean }) => {
        const pending = pendingProgressRef.current;
        if (!pending) return;
        pendingProgressRef.current = null;
        const seq = ++progressSaveSeqRef.current;
        if (options?.keepalive) {
          // The page may be unloading; fire a keepalive PUT so the write survives
          // teardown. The response cannot be observed, so skip cache updates.
          apiKeepalive(ebookProgressPath(contentID), {
            method: "PUT",
            body: JSON.stringify(pending),
          });
          return;
        }
        saveEbookReaderProgress(contentID, pending).then(
          (saved) => {
            // Saves can resolve out of order; only the newest may cache its response.
            if (seq === progressSaveSeqRef.current) {
              cacheEbookReaderProgress(queryClient, contentID, saved);
            }
          },
          () => {
            // Progress saves are best-effort; the next relocate retries.
          },
        );
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

      const flushProgressOnVisibilityChange = () => {
        if (document.visibilityState === "hidden") {
          // The page is still alive when it merely hides, so use the normal
          // authenticated path, which can refresh an expired access token.
          // Keepalive (no refresh possible) is reserved for pagehide.
          flushProgress();
        }
      };
      const flushProgressOnPageHide = () => flushProgress({ keepalive: true });

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
          openedBook = book as DisposableBookDoc;
          if (cancelled) {
            disposeOpenArtifacts();
            return;
          }

          await import("foliate-js/view.js");
          if (cancelled) {
            disposeOpenArtifacts();
            return;
          }
          const view = document.createElement("foliate-view") as FoliateViewElement;
          openedView = view;
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
          view.addEventListener("external-link", (event: Event) => {
            // The vendored foliate view would open the link itself via
            // globalThis.open(href, "_blank") with no noopener and no scheme
            // filter (reverse tabnabbing, javascript: URLs). Cancel its default
            // and open only safe schemes without an opener reference.
            event.preventDefault();
            const href = (event as CustomEvent<{ href?: unknown }>).detail?.href;
            if (typeof href !== "string") return;
            let scheme = "";
            try {
              scheme = new URL(href).protocol;
            } catch {
              return;
            }
            if (scheme === "http:" || scheme === "https:") {
              window.open(href, "_blank", "noopener,noreferrer");
            }
          });
          view.addEventListener("create-overlay", () => {
            attachSelectionListeners();
            drawAnnotations();
          });
          view.addEventListener("relocate", (event: Event) => {
            // Only the run that owns the live view may save progress for its file;
            // a superseded view firing late relocates must not overwrite it.
            if (!initializedRef.current || viewRef.current !== view) return;
            const detail = (event as CustomEvent<RelocateDetail>).detail;
            const progress = progressFromRelocate(detail, file.file_id);
            if (progress) {
              onProgressChange?.(progress.progress);
              scheduleProgressSave(progress);
            }
            const fraction = detail.fraction;
            // typeof narrows fraction to number for the Math calls below;
            // Number.isFinite on top of that rejects NaN/Infinity, which
            // `typeof` alone would let through as a "number".
            if (typeof fraction === "number" && Number.isFinite(fraction)) {
              onLocationChange?.({
                fraction: Math.min(1, Math.max(0, fraction)),
                sectionIndex: typeof detail.index === "number" ? detail.index : null,
                tocLabel: detail.tocItem?.label?.trim() || null,
              });
            }
          });
          await view.open(book);
          if (cancelled) {
            disposeOpenArtifacts();
            return;
          }
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
          if (cancelled) {
            disposeOpenArtifacts();
            return;
          }
          initializedRef.current = true;
          setLoading(false);
        } catch (err) {
          if (cancelled) {
            disposeOpenArtifacts();
            return;
          }
          setError(err instanceof Error ? err.message : "Unable to open ebook");
          setLoading(false);
        }
      }

      void open();
      const drawnCfis = drawnCfisRef.current;
      document.addEventListener("visibilitychange", flushProgressOnVisibilityChange);
      window.addEventListener("pagehide", flushProgressOnPageHide);
      return () => {
        cancelled = true;
        initializedRef.current = false;
        document.removeEventListener("visibilitychange", flushProgressOnVisibilityChange);
        window.removeEventListener("pagehide", flushProgressOnPageHide);
        if (saveTimerRef.current !== null) {
          window.clearTimeout(saveTimerRef.current);
          saveTimerRef.current = null;
        }
        flushProgress();
        disposeOpenArtifacts();
        appliedRendererKeyRef.current = "";
        for (const cleanup of selectionCleanupRef.current) cleanup();
        selectionCleanupRef.current = [];
        drawnCfis.clear();
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
      onLocationChange,
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
