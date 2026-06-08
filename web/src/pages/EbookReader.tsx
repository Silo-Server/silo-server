import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowLeft,
  Bookmark,
  BookOpen,
  ChevronLeft,
  ChevronRight,
  Download,
  Highlighter,
  Library,
  ListTree,
  Loader2,
  PanelRightClose,
  PanelRightOpen,
  Pause,
  Play,
  Search,
  Settings,
  StickyNote,
  Square,
  Trash2,
  Type,
  Volume2,
} from "lucide-react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";

import type { FileVersion } from "@/api/types";
import PageBack from "@/components/PageBack";
import { Button } from "@/components/ui/button";
import { useEinkMode } from "@/hooks/useEinkMode";
import { useScreenWakeLock } from "@/hooks/useScreenWakeLock";
import { useTTS } from "@/hooks/useTTS";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { cn } from "@/lib/utils";
import type { TOCItem } from "@/reader/readest/libs/document";
import FoliateBookReader, {
  DEFAULT_READER_SETTINGS,
  formatReaderProgress,
  isReaderSupportedFile,
  normalizeReaderSettings,
  readerFileFormat,
  type FoliateBookReaderHandle,
  type ReaderLoadState,
  type ReaderSearchResult,
  type ReaderSelection,
  type ReaderSettings,
} from "@/reader/FoliateBookReader";
import {
  createEbookReaderAnnotation,
  deleteEbookReaderAnnotation,
  fetchEbookReaderAnnotations,
  fetchEbookReaderConfig,
  saveEbookReaderConfig,
  type EbookReaderAnnotation,
} from "@/reader/ebookReaderApi";

export const EBOOK_READER_SETTINGS_STORAGE_KEY = "silo.ebook.reader.settings";

type ReaderPanel = "toc" | "search" | "notes" | "settings";

type TocEntry = TOCItem & {
  depth: number;
};

function chooseReaderFile(
  files: FileVersion[],
  requestedID: number | null,
): FileVersion | undefined {
  const requested = requestedID ? files.find((file) => file.file_id === requestedID) : undefined;
  if (requested && isReaderSupportedFile(requested)) return requested;
  return (
    files.find((file) => readerFileFormat(file) === "epub") ??
    files.find((file) => isReaderSupportedFile(file)) ??
    files[0]
  );
}

function readerFileLabel(file: FileVersion): string {
  const format = readerFileFormat(file).toUpperCase();
  const name = file.file_name || file.file_path?.split(/[\\/]/).pop() || `File ${file.file_id}`;
  return format ? `${format} · ${name}` : name;
}

function flattenToc(items: TOCItem[] = [], depth = 0): TocEntry[] {
  return items.flatMap((item) => [
    { ...item, depth },
    ...flattenToc(item.subitems ?? [], depth + 1),
  ]);
}

function readerStorage(): Storage | null {
  try {
    return window.localStorage ?? null;
  } catch {
    return null;
  }
}

export function loadStoredReaderSettings(): ReaderSettings {
  try {
    const raw = readerStorage()?.getItem(EBOOK_READER_SETTINGS_STORAGE_KEY);
    if (!raw) return DEFAULT_READER_SETTINGS;
    return normalizeReaderSettings(JSON.parse(raw) as Partial<ReaderSettings>);
  } catch {
    return DEFAULT_READER_SETTINGS;
  }
}

function saveReaderSettings(settings: ReaderSettings) {
  readerStorage()?.setItem(EBOOK_READER_SETTINGS_STORAGE_KEY, JSON.stringify(settings));
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  return ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
}

export default function EbookReader() {
  const { contentId = "" } = useParams<{ contentId: string }>();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const requestedFileID = Number(searchParams.get("file_id") || "");
  const libraryIdParam = searchParams.get("libraryId");
  const { data: item, isLoading, error } = useCatalogItemDetail(contentId || undefined);
  const selectedFile = useMemo(
    () =>
      chooseReaderFile(
        item?.versions ?? [],
        Number.isFinite(requestedFileID) ? requestedFileID : null,
      ),
    [item?.versions, requestedFileID],
  );
  const readerFiles = useMemo(
    () => (item?.versions ?? []).filter((file) => isReaderSupportedFile(file)),
    [item?.versions],
  );
  const format = readerFileFormat(selectedFile);
  const readerRef = useRef<FoliateBookReaderHandle>(null);
  const [loadedFile, setLoadedFile] = useState<ReaderLoadState | null>(null);
  const [readerProgress, setReaderProgress] = useState<number | null>(null);
  const [toc, setToc] = useState<TOCItem[]>([]);
  const [panelOpen, setPanelOpen] = useState(true);
  const [panel, setPanel] = useState<ReaderPanel>("toc");
  const [readerSettings, setReaderSettings] = useState<ReaderSettings>(() =>
    loadStoredReaderSettings(),
  );
  const [annotations, setAnnotations] = useState<EbookReaderAnnotation[]>([]);
  const [selection, setSelection] = useState<ReaderSelection | null>(null);
  const [wakeLockEnabled, setWakeLockEnabled] = useState(false);
  const [ttsRate, setTtsRate] = useState(1);
  const [ttsVoiceURI, setTtsVoiceURI] = useState("");
  const [einkEnabled, setEinkEnabled] = useEinkMode();
  const tts = useTTS();
  useScreenWakeLock(wakeLockEnabled);
  const configLoadedRef = useRef(false);
  const saveConfigTimerRef = useRef<number | null>(null);
  const [searchText, setSearchText] = useState("");
  const [searchResults, setSearchResults] = useState<ReaderSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const progressLabel = formatReaderProgress(readerProgress);
  const tocEntries = useMemo(() => flattenToc(toc), [toc]);
  const handleFileLoaded = useCallback((state: ReaderLoadState | null) => {
    setLoadedFile(state);
  }, []);
  const handleProgressChange = useCallback((progress: number | null) => {
    setReaderProgress(progress);
  }, []);
  const handleReaderReady = useCallback(({ toc: readyToc }: { toc: TOCItem[] }) => {
    setToc(readyToc);
  }, []);
  const reloadAnnotations = useCallback(async () => {
    if (!contentId) return;
    setAnnotations(await fetchEbookReaderAnnotations(contentId));
  }, [contentId]);
  const handleFileChange = useCallback(
    (fileID: string) => {
      if (!contentId) return;
      const nextParams = new URLSearchParams();
      nextParams.set("file_id", fileID);
      if (libraryIdParam) {
        nextParams.set("libraryId", libraryIdParam);
      }
      navigate(`/reader/ebook/${encodeURIComponent(contentId)}?${nextParams.toString()}`, {
        replace: true,
      });
    },
    [contentId, libraryIdParam, navigate],
  );
  const updateReaderSettings = useCallback(
    (next: Partial<ReaderSettings>) => {
      setReaderSettings((current) => {
        const merged = normalizeReaderSettings({ ...current, ...next });
        saveReaderSettings(merged);
        if (contentId && configLoadedRef.current) {
          if (saveConfigTimerRef.current !== null) {
            window.clearTimeout(saveConfigTimerRef.current);
          }
          saveConfigTimerRef.current = window.setTimeout(() => {
            saveConfigTimerRef.current = null;
            void saveEbookReaderConfig(contentId, { settings: merged });
          }, 400);
        }
        return merged;
      });
    },
    [contentId],
  );
  const handleSearchSubmit = useCallback(async () => {
    const query = searchText.trim();
    if (!query) {
      setSearchResults([]);
      readerRef.current?.clearSearch();
      return;
    }
    setSearching(true);
    try {
      setSearchResults(await (readerRef.current?.search(query) ?? Promise.resolve([])));
    } finally {
      setSearching(false);
    }
  }, [searchText]);
  const handleProgressScrub = useCallback((value: string) => {
    const next = Math.min(1, Math.max(0, Number(value) / 100));
    if (!Number.isFinite(next)) return;
    setReaderProgress(next);
    void readerRef.current?.goToFraction(next);
  }, []);
  const handleCreateHighlight = useCallback(async () => {
    if (!contentId || !selection) return;
    const created = await createEbookReaderAnnotation(contentId, {
      kind: "highlight",
      cfi_range: selection.cfi,
      selected_text: selection.selectedText,
      style: "highlight",
      color: "#facc15",
    });
    setAnnotations((current) => [created, ...current]);
    readerRef.current?.clearSelection();
    setSelection(null);
  }, [contentId, selection]);
  const handleCreateBookmark = useCallback(async () => {
    if (!contentId) return;
    const location = selection?.cfi || `fraction:${(readerProgress ?? 0).toFixed(6)}`;
    const created = await createEbookReaderAnnotation(contentId, {
      kind: "bookmark",
      location,
      note: item?.title || "Bookmark",
    });
    setAnnotations((current) => [created, ...current]);
    setPanel("notes");
  }, [contentId, item?.title, readerProgress, selection]);
  const handleDeleteAnnotation = useCallback(
    async (annotationID: string) => {
      if (!contentId) return;
      await deleteEbookReaderAnnotation(contentId, annotationID);
      setAnnotations((current) => current.filter((annotation) => annotation.id !== annotationID));
    },
    [contentId],
  );
  const handleSpeak = useCallback(() => {
    const text = readerRef.current?.getReadableText() ?? "";
    tts.speak(text, {
      rate: ttsRate,
      voiceURI: ttsVoiceURI || undefined,
    });
  }, [tts, ttsRate, ttsVoiceURI]);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || isEditableTarget(event.target)) return;
      if (event.key === "ArrowLeft") {
        readerRef.current?.prev();
      } else if (event.key === "ArrowRight") {
        readerRef.current?.next();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, []);

  useEffect(() => {
    if (!contentId) return;
    let cancelled = false;
    configLoadedRef.current = false;
    void fetchEbookReaderConfig(contentId)
      .then((config) => {
        if (cancelled) return;
        const settings =
          config.settings && typeof config.settings === "object" && !Array.isArray(config.settings)
            ? normalizeReaderSettings(config.settings as Partial<ReaderSettings>)
            : loadStoredReaderSettings();
        configLoadedRef.current = true;
        saveReaderSettings(settings);
        setReaderSettings(settings);
      })
      .catch(() => {
        if (!cancelled) {
          configLoadedRef.current = true;
        }
      });
    return () => {
      cancelled = true;
      if (saveConfigTimerRef.current !== null) {
        window.clearTimeout(saveConfigTimerRef.current);
        saveConfigTimerRef.current = null;
      }
    };
  }, [contentId]);

  useEffect(() => {
    void reloadAnnotations();
  }, [reloadAnnotations]);
  if (isLoading) {
    return (
      <div className="flex min-h-[70vh] items-center justify-center">
        <Loader2 className="text-muted-foreground size-8 animate-spin" />
      </div>
    );
  }

  if (error || !item || item.type !== "ebook") {
    return (
      <div className="page-shell py-10">
        <PageBack />
        <div className="text-muted-foreground mt-10 text-sm">Ebook not found.</div>
      </div>
    );
  }

  const backHref = `/item/${encodeURIComponent(item.content_id)}${
    libraryIdParam ? `?libraryId=${encodeURIComponent(libraryIdParam)}` : ""
  }`;

  if (!selectedFile) {
    return (
      <div className="page-shell py-10">
        <PageBack />
        <div className="text-muted-foreground mt-10 text-sm">No ebook files found.</div>
      </div>
    );
  }

  return (
    <div className="bg-background min-h-screen">
      <header className="border-border/70 bg-background/95 sticky top-0 z-20 border-b backdrop-blur">
        <div className="flex h-14 items-center gap-3 px-4">
          <Button asChild variant="ghost" size="icon" aria-label="Back to ebook">
            <Link to={backHref}>
              <ArrowLeft className="size-5" />
            </Link>
          </Button>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">{item.title}</div>
            <div className="text-muted-foreground truncate text-xs">{format.toUpperCase()}</div>
          </div>
          {progressLabel && (
            <div className="text-muted-foreground hidden min-w-12 text-center text-xs tabular-nums sm:block">
              {progressLabel}
            </div>
          )}
          {isReaderSupportedFile(selectedFile) && readerFiles.length > 1 && (
            <select
              aria-label="Reader file"
              value={selectedFile.file_id}
              onChange={(event) => handleFileChange(event.target.value)}
              className="border-border bg-background text-foreground focus-visible:border-ring focus-visible:ring-ring/50 hidden h-8 max-w-44 rounded-md border px-2 text-xs outline-none focus-visible:ring-[3px] sm:block"
            >
              {readerFiles.map((file) => (
                <option key={file.file_id} value={file.file_id}>
                  {readerFileLabel(file)}
                </option>
              ))}
            </select>
          )}
          <div className="flex shrink-0 items-center gap-1">
            {selection && (
              <Button
                variant="secondary"
                size="sm"
                aria-label="Highlight selection"
                title="Highlight selection"
                onClick={() => void handleCreateHighlight()}
              >
                <Highlighter className="size-4" />
                Highlight
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Add bookmark"
              title="Add bookmark"
              onClick={() => void handleCreateBookmark()}
            >
              <Bookmark className="size-4" />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={panelOpen ? "Close reader panel" : "Open reader panel"}
              title={panelOpen ? "Close reader panel" : "Open reader panel"}
              onClick={() => setPanelOpen((open) => !open)}
            >
              {panelOpen ? (
                <PanelRightClose className="size-4" />
              ) : (
                <PanelRightOpen className="size-4" />
              )}
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Previous page"
              title="Previous page"
              onClick={() => readerRef.current?.prev()}
            >
              <ChevronLeft className="size-5" />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Next page"
              title="Next page"
              onClick={() => readerRef.current?.next()}
            >
              <ChevronRight className="size-5" />
            </Button>
          </div>
          {loadedFile && (
            <Button asChild variant="outline" size="sm">
              <a href={loadedFile.objectUrl} download={loadedFile.filename}>
                <Download className="size-4" />
                File
              </a>
            </Button>
          )}
        </div>
        <div className="border-border/60 flex h-10 items-center gap-3 border-t px-4">
          <BookOpen className="text-muted-foreground size-4 shrink-0" />
          <input
            aria-label="Reading progress"
            type="range"
            min="0"
            max="100"
            step="1"
            value={Math.round((readerProgress ?? 0) * 100)}
            onChange={(event) => handleProgressScrub(event.target.value)}
            className="accent-primary h-2 min-w-0 flex-1"
          />
          <div className="text-muted-foreground w-11 text-right text-xs tabular-nums">
            {progressLabel ?? "0%"}
          </div>
        </div>
      </header>

      <main
        className={cn(
          "grid h-[calc(100vh-6rem)] min-h-0",
          panelOpen ? "grid-cols-1 lg:grid-cols-[minmax(0,1fr)_20rem]" : "grid-cols-1",
        )}
      >
        {isReaderSupportedFile(selectedFile) ? (
          <section className="min-h-0">
            <FoliateBookReader
              ref={readerRef}
              contentID={contentId}
              file={selectedFile}
              title={item.title}
              settings={readerSettings}
              annotations={annotations}
              onFileLoaded={handleFileLoaded}
              onProgressChange={handleProgressChange}
              onReady={handleReaderReady}
              onSelectionChange={setSelection}
            />
          </section>
        ) : (
          <div className="flex h-full items-center justify-center px-6">
            <div className="max-w-md text-center">
              <Library className="text-muted-foreground mx-auto mb-4 size-10" />
              <h1 className="text-lg font-semibold">{item.title}</h1>
              <p className="text-muted-foreground mt-2 text-sm">Unsupported ebook format.</p>
            </div>
          </div>
        )}
        {panelOpen && isReaderSupportedFile(selectedFile) && (
          <aside className="border-border bg-background min-h-0 border-t lg:border-t-0 lg:border-l">
            <div className="border-border/70 flex h-11 items-center border-b px-2">
              {[
                {
                  id: "toc" as const,
                  label: "Contents",
                  icon: ListTree,
                  aria: "Table of contents",
                },
                { id: "search" as const, label: "Search", icon: Search, aria: "Search book" },
                {
                  id: "notes" as const,
                  label: "Notes",
                  icon: StickyNote,
                  aria: "Annotations and bookmarks",
                },
                {
                  id: "settings" as const,
                  label: "Settings",
                  icon: Settings,
                  aria: "Reader settings",
                },
              ].map((tab) => {
                const Icon = tab.icon;
                return (
                  <Button
                    key={tab.id}
                    variant={panel === tab.id ? "secondary" : "ghost"}
                    size="sm"
                    aria-label={tab.aria}
                    title={tab.label}
                    onClick={() => setPanel(tab.id)}
                    className="flex-1"
                  >
                    <Icon className="size-4" />
                    <span className="hidden xl:inline">{tab.label}</span>
                  </Button>
                );
              })}
            </div>

            <div className="h-[calc(100%-2.75rem)] overflow-y-auto p-3">
              {panel === "toc" && (
                <div className="space-y-1">
                  {tocEntries.length === 0 ? (
                    <div className="text-muted-foreground px-2 py-8 text-center text-sm">
                      No contents found.
                    </div>
                  ) : (
                    tocEntries.map((entry) => (
                      <Button
                        key={`${entry.id}-${entry.href}`}
                        variant="ghost"
                        size="sm"
                        onClick={() => readerRef.current?.goTo(entry.href)}
                        className="h-auto w-full justify-start py-2 text-left text-sm whitespace-normal"
                        style={{ paddingLeft: `${0.5 + entry.depth * 1}rem` }}
                      >
                        {entry.label}
                      </Button>
                    ))
                  )}
                </div>
              )}

              {panel === "search" && (
                <div className="space-y-3">
                  <form
                    className="flex gap-2"
                    onSubmit={(event) => {
                      event.preventDefault();
                      void handleSearchSubmit();
                    }}
                  >
                    <input
                      aria-label="Search text"
                      value={searchText}
                      onChange={(event) => setSearchText(event.target.value)}
                      className="border-border bg-background focus-visible:border-ring focus-visible:ring-ring/50 h-9 min-w-0 flex-1 rounded-md border px-3 text-sm outline-none focus-visible:ring-[3px]"
                    />
                    <Button
                      aria-label="Run search"
                      title="Run search"
                      size="icon"
                      disabled={searching}
                    >
                      {searching ? (
                        <Loader2 className="size-4 animate-spin" />
                      ) : (
                        <Search className="size-4" />
                      )}
                    </Button>
                  </form>
                  <div className="space-y-1">
                    {searchResults.map((result) => (
                      <Button
                        key={result.cfi}
                        variant="ghost"
                        size="sm"
                        onClick={() => readerRef.current?.goTo(result.cfi)}
                        className="h-auto w-full justify-start py-2 text-left whitespace-normal"
                      >
                        <span className="min-w-0">
                          {result.label && (
                            <span className="text-muted-foreground block text-xs">
                              {result.label}
                            </span>
                          )}
                          <span className="block text-sm">{result.excerpt || result.cfi}</span>
                        </span>
                      </Button>
                    ))}
                  </div>
                </div>
              )}

              {panel === "notes" && (
                <div className="space-y-2">
                  {annotations.length === 0 ? (
                    <div className="text-muted-foreground px-2 py-8 text-center text-sm">
                      No annotations yet.
                    </div>
                  ) : (
                    annotations.map((annotation) => (
                      <div
                        key={annotation.id}
                        className="border-border rounded-md border p-2 text-sm"
                      >
                        <div className="flex items-start gap-2">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() =>
                              readerRef.current?.goTo(
                                annotation.cfi_range || annotation.location || "",
                              )
                            }
                            className="h-auto min-w-0 flex-1 justify-start px-1 py-1 text-left whitespace-normal"
                          >
                            <span className="min-w-0">
                              <span className="text-muted-foreground block text-xs capitalize">
                                {annotation.kind}
                              </span>
                              <span className="block">
                                {annotation.selected_text || annotation.note || annotation.location}
                              </span>
                            </span>
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            aria-label="Delete annotation"
                            title="Delete annotation"
                            onClick={() => void handleDeleteAnnotation(annotation.id)}
                          >
                            <Trash2 className="size-3" />
                          </Button>
                        </div>
                      </div>
                    ))
                  )}
                </div>
              )}

              {panel === "settings" && (
                <div className="space-y-4">
                  <div className="space-y-3">
                    <div className="flex items-center gap-2 text-sm font-medium">
                      <Volume2 className="size-4" />
                      Read aloud
                    </div>
                    <div className="flex gap-2">
                      <Button
                        variant="secondary"
                        size="sm"
                        aria-label="Speak text"
                        onClick={handleSpeak}
                      >
                        <Play className="size-4" />
                        Speak
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={tts.state === "paused" ? "Resume speech" : "Pause speech"}
                        onClick={tts.state === "paused" ? tts.resume : tts.pause}
                      >
                        <Pause className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="Stop speech"
                        onClick={tts.stop}
                      >
                        <Square className="size-4" />
                      </Button>
                    </div>
                    <ReaderRange
                      label="Speech rate"
                      value={ttsRate}
                      min={0.5}
                      max={2}
                      step={0.1}
                      onChange={setTtsRate}
                    />
                    <label className="block space-y-1 text-sm">
                      <span className="text-muted-foreground text-xs font-medium">Voice</span>
                      <select
                        aria-label="Voice"
                        value={ttsVoiceURI}
                        onChange={(event) => setTtsVoiceURI(event.target.value)}
                        className="border-border bg-background focus-visible:border-ring focus-visible:ring-ring/50 h-9 w-full rounded-md border px-2 text-sm outline-none focus-visible:ring-[3px]"
                      >
                        <option value="">Default</option>
                        {tts.voices.map((voice) => (
                          <option key={voice.voiceURI} value={voice.voiceURI}>
                            {voice.name}
                          </option>
                        ))}
                      </select>
                    </label>
                  </div>
                  <div className="border-border space-y-2 border-t pt-3">
                    <label className="flex items-center justify-between gap-3 text-sm">
                      <span>Keep screen awake</span>
                      <input
                        aria-label="Keep screen awake"
                        type="checkbox"
                        checked={wakeLockEnabled}
                        onChange={(event) => setWakeLockEnabled(event.target.checked)}
                      />
                    </label>
                    <label className="flex items-center justify-between gap-3 text-sm">
                      <span>E-ink mode</span>
                      <input
                        aria-label="E-ink mode"
                        type="checkbox"
                        checked={einkEnabled}
                        onChange={(event) => setEinkEnabled(event.target.checked)}
                      />
                    </label>
                  </div>
                  <div className="flex items-center gap-2 text-sm font-medium">
                    <Type className="size-4" />
                    Typography
                  </div>
                  <label className="block space-y-1 text-sm">
                    <span className="text-muted-foreground text-xs font-medium">Theme</span>
                    <select
                      aria-label="Theme"
                      value={readerSettings.theme}
                      onChange={(event) =>
                        updateReaderSettings({
                          theme: event.target.value as ReaderSettings["theme"],
                        })
                      }
                      className="border-border bg-background focus-visible:border-ring focus-visible:ring-ring/50 h-9 w-full rounded-md border px-2 text-sm outline-none focus-visible:ring-[3px]"
                    >
                      <option value="light">Light</option>
                      <option value="sepia">Sepia</option>
                      <option value="dark">Dark</option>
                    </select>
                  </label>
                  <label className="block space-y-1 text-sm">
                    <span className="text-muted-foreground text-xs font-medium">Font</span>
                    <select
                      aria-label="Font family"
                      value={readerSettings.fontFamily}
                      onChange={(event) => updateReaderSettings({ fontFamily: event.target.value })}
                      className="border-border bg-background focus-visible:border-ring focus-visible:ring-ring/50 h-9 w-full rounded-md border px-2 text-sm outline-none focus-visible:ring-[3px]"
                    >
                      <option value="Inter, ui-sans-serif, system-ui, sans-serif">Inter</option>
                      <option value="Georgia, serif">Georgia</option>
                      <option value="Merriweather, Georgia, serif">Merriweather</option>
                      <option value="ui-serif, Georgia, Cambria, serif">System Serif</option>
                    </select>
                  </label>
                  <ReaderRange
                    label="Font size"
                    value={readerSettings.fontSize}
                    min={80}
                    max={180}
                    step={1}
                    suffix="%"
                    onChange={(fontSize) => updateReaderSettings({ fontSize })}
                  />
                  <ReaderRange
                    label="Line height"
                    value={readerSettings.lineHeight}
                    min={1.1}
                    max={2.4}
                    step={0.05}
                    onChange={(lineHeight) => updateReaderSettings({ lineHeight })}
                  />
                  <ReaderRange
                    label="Margin"
                    value={readerSettings.margin}
                    min={0}
                    max={64}
                    step={1}
                    suffix="px"
                    onChange={(margin) => updateReaderSettings({ margin })}
                  />
                  <ReaderRange
                    label="Width"
                    value={readerSettings.maxWidth}
                    min={42}
                    max={96}
                    step={1}
                    suffix="ch"
                    onChange={(maxWidth) => updateReaderSettings({ maxWidth })}
                  />
                  <label className="block space-y-1 text-sm">
                    <span className="text-muted-foreground text-xs font-medium">Spread</span>
                    <select
                      aria-label="Spread"
                      value={readerSettings.spread}
                      onChange={(event) =>
                        updateReaderSettings({
                          spread: event.target.value as ReaderSettings["spread"],
                        })
                      }
                      className="border-border bg-background focus-visible:border-ring focus-visible:ring-ring/50 h-9 w-full rounded-md border px-2 text-sm outline-none focus-visible:ring-[3px]"
                    >
                      <option value="auto">Auto</option>
                      <option value="none">Single page</option>
                    </select>
                  </label>
                  <label className="block space-y-1 text-sm">
                    <span className="text-muted-foreground text-xs font-medium">Flow</span>
                    <select
                      aria-label="Flow"
                      value={readerSettings.flow}
                      onChange={(event) =>
                        updateReaderSettings({ flow: event.target.value as ReaderSettings["flow"] })
                      }
                      className="border-border bg-background focus-visible:border-ring focus-visible:ring-ring/50 h-9 w-full rounded-md border px-2 text-sm outline-none focus-visible:ring-[3px]"
                    >
                      <option value="paginated">Paginated</option>
                      <option value="scrolled">Scrolled</option>
                    </select>
                  </label>
                </div>
              )}
            </div>
          </aside>
        )}
      </main>
    </div>
  );
}

type ReaderRangeProps = {
  label: string;
  value: number;
  min: number;
  max: number;
  step: number;
  suffix?: string;
  onChange: (value: number) => void;
};

function ReaderRange({ label, value, min, max, step, suffix = "", onChange }: ReaderRangeProps) {
  return (
    <label className="block space-y-1 text-sm">
      <span className="text-muted-foreground flex items-center justify-between gap-3 text-xs font-medium">
        <span>{label}</span>
        <span className="tabular-nums">
          {Number.isInteger(step) ? value : value.toFixed(2)}
          {suffix}
        </span>
      </span>
      <input
        aria-label={label}
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(event) => onChange(Number(event.target.value))}
        className="accent-primary h-2 w-full"
      />
    </label>
  );
}
