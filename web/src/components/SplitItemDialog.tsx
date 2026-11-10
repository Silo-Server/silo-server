import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Folder, Search, Scissors } from "lucide-react";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type {
  ItemFile,
  ItemMatchSearchRequest,
  ItemSplitRequest,
  ItemSplitResponse,
  MatchCandidate,
  SplitHistoryMode,
} from "@/api/types";
import { ItemLibraryPicker } from "@/components/ItemLibraryPicker";
import { useItemFiles, useSearchItemMatchCandidates, useSplitItem } from "@/hooks/queries/items";
import { useT, type MessageKey } from "@/i18n";
import { libraryIdsOfFiles } from "@/lib/itemLibraries";
import { cn } from "@/lib/utils";

interface SplittableItem {
  content_id: string;
  title: string;
  year?: number;
  type: string;
  /** The library the page is scoped to, if any. */
  library_id?: number;
}

interface SplitItemDialogProps {
  item: SplittableItem;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

const HISTORY_MODE_LABELS: Record<SplitHistoryMode, MessageKey> = {
  evidence: "web.item.split.historyEvidence",
  keep: "web.item.split.historyKeep",
  move_all: "web.item.split.historyMoveAll",
};

/**
 * Repairs a wrong merge: moves selected files (usually one folder) of this
 * item to a different — possibly new — item, previews the watch-state
 * reattribution via a dry run, and persists identity overrides so rescans
 * keep the corrected assignment.
 */
export default function SplitItemDialog({ item, open, onOpenChange }: SplitItemDialogProps) {
  const t = useT();
  const { data: filesData, isLoading: filesLoading } = useItemFiles(
    open ? item.content_id : undefined,
  );
  const files = useMemo(() => filesData?.files ?? [], [filesData]);

  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [title, setTitle] = useState("");
  const [year, setYear] = useState("");
  const [imdbId, setImdbId] = useState("");
  const [tmdbId, setTmdbId] = useState("");
  const [tvdbId, setTvdbId] = useState("");
  const [selectedCandidate, setSelectedCandidate] = useState<MatchCandidate | null>(null);
  const [detachUnmatched, setDetachUnmatched] = useState(false);
  const [historyMode, setHistoryMode] = useState<SplitHistoryMode>("evidence");
  const [preview, setPreview] = useState<ItemSplitResponse | null>(null);
  const [pickedLibraryId, setPickedLibraryId] = useState<number>();

  const searchMutation = useSearchItemMatchCandidates(item.content_id);
  const splitMutation = useSplitItem();
  const candidates = searchMutation.data?.candidates ?? [];

  // Monotonic token so a slow dry-run response for an outdated plan can't
  // overwrite the preview of the current one.
  const previewToken = useRef(0);

  const filesByRoot = useMemo(() => {
    const groups = new Map<string, ItemFile[]>();
    for (const file of files) {
      const group = groups.get(file.observed_root_path) ?? [];
      group.push(file);
      groups.set(file.observed_root_path, group);
    }
    return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [files]);

  // The split itself needs no library, but the identity search does when the
  // item is in several: the files being moved say which one, then the page's
  // library scope, then the admin's pick. A single-library item searches
  // without one, as the server resolves it.
  const itemLibraryIds = useMemo(() => libraryIdsOfFiles(files), [files]);
  const selectedLibraryIds = useMemo(
    () => libraryIdsOfFiles(files.filter((file) => selectedIds.has(file.id))),
    [files, selectedIds],
  );
  const multiLibrary = itemLibraryIds.length > 1;
  const inferredLibraryId = !multiLibrary
    ? undefined
    : selectedLibraryIds.length === 1
      ? selectedLibraryIds[0]
      : item.library_id;
  const currentPick =
    pickedLibraryId !== undefined && itemLibraryIds.includes(pickedLibraryId)
      ? pickedLibraryId
      : undefined;
  const needsLibraryPick = multiLibrary && inferredLibraryId === undefined;
  const searchLibraryId = needsLibraryPick ? currentPick : inferredLibraryId;
  const searchBlocked = filesLoading || (needsLibraryPick && currentPick === undefined);

  const toggleFile = useCallback((id: string) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }, []);

  const toggleRoot = useCallback((rootFiles: ItemFile[]) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      const allSelected = rootFiles.every((file) => next.has(file.id));
      for (const file of rootFiles) {
        if (allSelected) {
          next.delete(file.id);
        } else {
          next.add(file.id);
        }
      }
      return next;
    });
  }, []);

  const handleSearch = useCallback(() => {
    setSelectedCandidate(null);
    setDetachUnmatched(false);
    const parsedYear = Number.parseInt(year.trim(), 10);
    const request: ItemMatchSearchRequest = {
      title: title || undefined,
      year: Number.isFinite(parsedYear) ? parsedYear : undefined,
      imdb_id: imdbId || undefined,
      tmdb_id: tmdbId || undefined,
      tvdb_id: tvdbId || undefined,
      library_id: searchLibraryId,
    };
    searchMutation.mutate(request);
  }, [title, year, imdbId, tmdbId, tvdbId, searchLibraryId, searchMutation]);

  const targetChosen = detachUnmatched || selectedCandidate !== null;
  const selectionValid = selectedIds.size > 0 && selectedIds.size < files.length;
  const planValid = selectionValid && targetChosen;

  const buildRequest = useCallback(
    (dryRun: boolean): ItemSplitRequest => ({
      file_ids: [...selectedIds],
      target: detachUnmatched
        ? { unmatched: true }
        : {
            provider_ids: selectedCandidate?.provider_ids,
            title: selectedCandidate?.title,
            year: selectedCandidate?.year || undefined,
          },
      history_mode: historyMode,
      dry_run: dryRun,
    }),
    [selectedIds, detachUnmatched, selectedCandidate, historyMode],
  );

  // react-query guarantees `mutate` is referentially stable, so the effect
  // below only re-runs when the plan itself changes.
  const { mutate: runSplit } = splitMutation;

  // Any change to the plan invalidates the previous preview; once the plan is
  // valid, a debounced dry run fetches a fresh one automatically.
  useEffect(() => {
    previewToken.current += 1;
    setPreview(null);
    if (!open || !planValid) return;
    const token = previewToken.current;
    const timer = setTimeout(() => {
      runSplit(
        { contentId: item.content_id, request: buildRequest(true) },
        {
          onSuccess: (result) => {
            if (token === previewToken.current) setPreview(result);
          },
        },
      );
    }, 400);
    return () => clearTimeout(timer);
  }, [open, planValid, buildRequest, runSplit, item.content_id]);

  const confirmSplit = useCallback(() => {
    runSplit(
      { contentId: item.content_id, request: buildRequest(false) },
      { onSuccess: () => onOpenChange(false) },
    );
  }, [runSplit, item.content_id, buildRequest, onOpenChange]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] max-w-2xl flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>{t("web.item.split.title")}</DialogTitle>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto pe-1">
          <div className="bg-muted/50 shrink-0 rounded-lg px-3 py-2 text-sm">
            <span className="font-medium">{item.title}</span>
            {item.year ? (
              <span className="text-muted-foreground ms-2">
                {t("web.item.dialog.yearInParens", { year: String(item.year) })}
              </span>
            ) : null}
            <Badge variant="secondary" className="ms-2 text-[10px]">
              {item.type}
            </Badge>
          </div>

          {/* Step 1: pick the files that belong to a different title */}
          <section className="space-y-2">
            <Label>{t("web.item.split.filesToMove")}</Label>
            {filesLoading ? (
              <div className="text-muted-foreground bg-muted/30 rounded-lg border px-3 py-2 text-sm">
                {t("web.item.split.loadingFiles")}
              </div>
            ) : files.length < 2 ? (
              <div className="text-muted-foreground bg-muted/30 rounded-lg border px-3 py-2 text-sm">
                {t("web.item.split.oneFile")}
              </div>
            ) : (
              <div className="bg-background/70 divide-border/50 divide-y rounded-lg border">
                {filesByRoot.map(([root, rootFiles]) => {
                  const allSelected = rootFiles.every((file) => selectedIds.has(file.id));
                  return (
                    <div key={root} className="px-3 py-2">
                      <label className="flex cursor-pointer items-center gap-2">
                        <input
                          type="checkbox"
                          checked={allSelected}
                          onChange={() => toggleRoot(rootFiles)}
                          aria-label={t("web.item.split.selectFolder", { folder: root })}
                        />
                        <Folder className="text-muted-foreground h-3.5 w-3.5 shrink-0" />
                        <span className="min-w-0 flex-1 truncate font-mono text-xs" title={root}>
                          {root}
                        </span>
                      </label>
                      <div className="mt-1 space-y-1 ps-6">
                        {rootFiles.map((file) => (
                          <label
                            key={file.id}
                            className="flex cursor-pointer items-center gap-2 text-xs"
                          >
                            <input
                              type="checkbox"
                              checked={selectedIds.has(file.id)}
                              onChange={() => toggleFile(file.id)}
                              aria-label={t("web.item.split.selectFile", { path: file.file_path })}
                            />
                            <span
                              className="text-muted-foreground min-w-0 flex-1 truncate font-mono"
                              title={file.file_path}
                            >
                              {file.file_path.split("/").pop()}
                            </span>
                            {file.season_number && file.episode_number ? (
                              <Badge variant="outline" className="text-[10px]">
                                {t("web.item.split.episodeCode", {
                                  season: String(file.season_number),
                                  episode: String(file.episode_number),
                                })}
                              </Badge>
                            ) : null}
                          </label>
                        ))}
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
            {selectedIds.size > 0 && selectedIds.size === files.length && (
              <p className="text-destructive text-xs">{t("web.item.split.allSelected")}</p>
            )}
          </section>

          {/* Step 2: what the moved files actually are */}
          <section className="space-y-3">
            <Label>{t("web.item.split.identity")}</Label>
            <div className="grid grid-cols-2 gap-3">
              <div className="col-span-2">
                <Input
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  placeholder={t("web.item.dialog.fieldTitle")}
                  aria-label={t("web.item.split.searchTitle")}
                />
              </div>
              <Input
                value={year}
                onChange={(e) => setYear(e.target.value)}
                placeholder={t("web.item.dialog.fieldYear")}
                type="number"
                aria-label={t("web.item.split.searchYear")}
              />
              <Input
                value={tmdbId}
                onChange={(e) => setTmdbId(e.target.value)}
                placeholder={t("web.item.dialog.tmdbId")}
                aria-label={t("web.item.dialog.tmdbId")}
              />
              <Input
                value={imdbId}
                onChange={(e) => setImdbId(e.target.value)}
                placeholder={t("web.item.split.imdbPlaceholder")}
                aria-label={t("web.item.dialog.imdbId")}
              />
              <Input
                value={tvdbId}
                onChange={(e) => setTvdbId(e.target.value)}
                placeholder={t("web.item.dialog.tvdbId")}
                aria-label={t("web.item.dialog.tvdbId")}
              />
            </div>
            {needsLibraryPick && (
              <ItemLibraryPicker
                libraryIds={itemLibraryIds}
                value={currentPick}
                onChange={setPickedLibraryId}
              />
            )}
            <Button
              onClick={handleSearch}
              disabled={searchMutation.isPending || searchBlocked}
              variant="secondary"
              className="w-full gap-2"
            >
              <Search className={cn("h-4 w-4", searchMutation.isPending && "animate-spin")} />
              {t("web.item.dialog.search")}
            </Button>

            {candidates.length > 0 && (
              <div className="max-h-56 space-y-1 overflow-y-auto pe-1">
                {candidates.map((candidate, index) => {
                  const key = Object.entries(candidate.provider_ids)
                    .map(([k, v]) => `${k}-${v}`)
                    .join("_");
                  return (
                    <button
                      key={`${key}-${index}`}
                      type="button"
                      className={cn(
                        "flex w-full min-w-0 items-center gap-3 rounded-lg border p-2 text-start transition-colors",
                        selectedCandidate === candidate
                          ? "border-primary bg-primary/5"
                          : "border-border hover:bg-muted/50",
                      )}
                      onClick={() => {
                        setSelectedCandidate(candidate);
                        setDetachUnmatched(false);
                      }}
                      data-testid="split-candidate"
                    >
                      {candidate.image_url ? (
                        <img
                          src={candidate.image_url}
                          alt=""
                          className="h-14 w-10 shrink-0 rounded object-cover"
                        />
                      ) : (
                        <div className="bg-muted h-14 w-10 shrink-0 rounded" />
                      )}
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">{candidate.title}</div>
                        <div className="text-muted-foreground text-xs">{candidate.year || ""}</div>
                      </div>
                    </button>
                  );
                })}
              </div>
            )}
            {searchMutation.isSuccess && candidates.length === 0 && (
              <p className="text-muted-foreground text-center text-sm">
                {t("web.item.dialog.noCandidates")}
              </p>
            )}

            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={detachUnmatched}
                onChange={(e) => {
                  setDetachUnmatched(e.target.checked);
                  if (e.target.checked) setSelectedCandidate(null);
                }}
              />
              {t("web.item.split.detachUnmatched")}
            </label>
          </section>

          {/* Step 3: watch-state handling */}
          <section className="space-y-2">
            <Label>{t("web.item.split.historyHandling")}</Label>
            <Select
              value={historyMode}
              onValueChange={(value) => setHistoryMode(value as SplitHistoryMode)}
            >
              <SelectTrigger aria-label={t("web.item.split.historyHandling")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(Object.keys(HISTORY_MODE_LABELS) as SplitHistoryMode[]).map((mode) => (
                  <SelectItem key={mode} value={mode}>
                    {t(HISTORY_MODE_LABELS[mode])}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-xs">{t("web.item.split.historyHelp")}</p>
          </section>

          {planValid && !preview && (
            <section className="bg-muted/30 text-muted-foreground rounded-lg border px-3 py-2 text-sm">
              {t("web.item.split.previewing")}
            </section>
          )}
          {preview && (
            <section className="bg-muted/30 space-y-1 rounded-lg border px-3 py-2 text-sm">
              <div className="font-medium">
                {t("web.item.split.previewHeading", {
                  count: preview.files_moved,
                  file_count: String(preview.files_moved),
                  target_id: preview.target_content_id,
                  id: (chunks) => <span className="font-mono text-xs">{chunks}</span>,
                })}
                {preview.target_created ? (
                  <Badge variant="secondary" className="ms-2 text-[10px]">
                    {t("web.item.split.newItem")}
                  </Badge>
                ) : null}
              </div>
              <ul className="text-muted-foreground list-inside list-disc text-xs">
                <li>
                  {t("web.item.split.resumePoints", {
                    count: String(preview.reattribution.progress_moved),
                  })}
                </li>
                <li>
                  {t("web.item.split.historyEntries", {
                    moved: String(preview.reattribution.history_moved),
                    ambiguous: String(preview.reattribution.history_ambiguous),
                  })}
                </li>
                <li>
                  {t("web.item.split.downloads", {
                    count: String(preview.reattribution.downloads),
                  })}
                </li>
                {preview.episode_pairs > 0 && (
                  <li>
                    {t("web.item.split.episodesReanchored", {
                      count: String(preview.episode_pairs),
                    })}
                  </li>
                )}
                {(preview.root_overrides?.length ?? 0) + (preview.file_overrides?.length ?? 0) >
                  0 && (
                  <li>
                    {t("web.item.split.overridesPinned", {
                      folders: String(preview.root_overrides?.length ?? 0),
                      files: String(preview.file_overrides?.length ?? 0),
                    })}
                  </li>
                )}
              </ul>
            </section>
          )}
        </div>

        <div className="border-border/50 flex shrink-0 border-t pt-4">
          <Button
            className="flex-1 gap-2"
            onClick={confirmSplit}
            disabled={!planValid || !preview || splitMutation.isPending}
            data-testid="confirm-split"
          >
            <Scissors className="h-4 w-4" />
            {splitMutation.isPending && preview
              ? t("web.item.split.splitting")
              : t("web.item.split.split")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
