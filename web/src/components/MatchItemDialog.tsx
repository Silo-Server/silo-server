import { useState, useCallback, useMemo, useRef } from "react";
import { Copy, Folder, Plus, Search, X } from "lucide-react";
import { toast } from "sonner";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import type { FileVersion, ItemDetail, ItemMatchSearchRequest, MatchCandidate } from "@/api/types";
import MediaLocations from "@/components/MediaLocations";
import { useSearchItemMatchCandidates, useApplyItemMatch } from "@/hooks/queries/items";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { cn } from "@/lib/utils";

type MatchableItem = Pick<
  ItemDetail,
  "content_id" | "title" | "year" | "series_id" | "season_number"
> & {
  type: string;
  library_id?: number;
  versions?: FileVersion[];
  folder_paths?: string[];
};

type ProviderIDInput = {
  id: number;
  provider: string;
  value: string;
};

interface MatchItemDialogProps {
  item: MatchableItem;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function isVideoMatchType(type: string): boolean {
  switch (type.trim().toLowerCase()) {
    case "movie":
    case "movies":
    case "series":
    case "show":
    case "shows":
    case "tv":
    case "season":
    case "seasons":
    case "episode":
    case "episodes":
      return true;
    default:
      return false;
  }
}

export default function MatchItemDialog({ item, open, onOpenChange }: MatchItemDialogProps) {
  const [title, setTitle] = useState(item.title);
  const [year, setYear] = useState(item.year ? String(item.year) : "");
  const [imdbId, setImdbId] = useState("");
  const [tmdbId, setTmdbId] = useState("");
  const [tvdbId, setTvdbId] = useState("");
  const [providerIdInputs, setProviderIdInputs] = useState<ProviderIDInput[]>([
    { id: 0, provider: "", value: "" },
  ]);
  const nextProviderIdInputId = useRef(1);
  const [selectedCandidate, setSelectedCandidate] = useState<MatchCandidate | null>(null);
  const isSeries = item.type === "series";
  const showVideoExternalIds = isVideoMatchType(item.type);
  const needsItemDetail = open && item.versions === undefined;
  const { data: enrichedItem, isLoading: enrichedItemLoading } = useCatalogItemDetail(
    needsItemDetail ? item.content_id : undefined,
    needsItemDetail ? item.library_id : undefined,
  );

  const searchMutation = useSearchItemMatchCandidates(item.content_id);
  const applyMutation = useApplyItemMatch();

  const candidates = searchMutation.data?.candidates ?? [];
  const effectiveItem = needsItemDetail ? (enrichedItem ?? item) : item;
  const genericProviderIds = useMemo(() => {
    if (showVideoExternalIds) return {};

    return providerIdInputs.reduce<Record<string, string>>((acc, entry) => {
      const provider = entry.provider.trim().toLowerCase();
      const value = entry.value.trim();
      if (provider && value) {
        acc[provider] = value;
      }
      return acc;
    }, {});
  }, [providerIdInputs, showVideoExternalIds]);

  const handleSearch = useCallback(() => {
    setSelectedCandidate(null);
    const normalizedYear = year.trim();
    const parsedYear = normalizedYear === "" ? undefined : Number.parseInt(normalizedYear, 10);
    const request: ItemMatchSearchRequest = {
      title: title || undefined,
      year: parsedYear !== undefined && Number.isFinite(parsedYear) ? parsedYear : undefined,
      library_id: item.library_id,
    };

    if (showVideoExternalIds) {
      request.imdb_id = imdbId || undefined;
      request.tmdb_id = tmdbId || undefined;
      request.tvdb_id = tvdbId || undefined;
    } else if (Object.keys(genericProviderIds).length > 0) {
      request.provider_ids = genericProviderIds;
    }

    searchMutation.mutate(request);
  }, [
    title,
    year,
    item.library_id,
    showVideoExternalIds,
    imdbId,
    tmdbId,
    tvdbId,
    genericProviderIds,
    searchMutation,
  ]);

  const updateProviderIdInput = useCallback(
    (index: number, field: keyof ProviderIDInput, value: string) => {
      setProviderIdInputs((current) =>
        current.map((entry, entryIndex) =>
          entryIndex === index ? { ...entry, [field]: value } : entry,
        ),
      );
    },
    [],
  );

  const addProviderIdInput = useCallback(() => {
    const id = nextProviderIdInputId.current;
    nextProviderIdInputId.current += 1;
    setProviderIdInputs((current) => [...current, { id, provider: "", value: "" }]);
  }, []);

  const removeProviderIdInput = useCallback((index: number) => {
    setProviderIdInputs((current) => {
      const next = current.filter((_, entryIndex) => entryIndex !== index);
      return next.length > 0 ? next : [{ id: 0, provider: "", value: "" }];
    });
  }, []);

  const handleApply = useCallback(() => {
    if (!selectedCandidate) return;
    applyMutation.mutate(
      { item, providerIds: selectedCandidate.provider_ids },
      {
        onSuccess: () => {
          onOpenChange(false);
        },
      },
    );
  }, [selectedCandidate, applyMutation, item, onOpenChange]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] max-w-2xl flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>Match Item</DialogTitle>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-hidden">
          {/* Current item summary */}
          <div className="bg-muted/50 shrink-0 rounded-lg px-3 py-2 text-sm">
            <span className="font-medium">{item.title}</span>
            {item.year ? <span className="text-muted-foreground ml-2">({item.year})</span> : null}
            <Badge variant="secondary" className="ml-2 text-[10px]">
              {item.type}
            </Badge>
          </div>

          {(enrichedItemLoading ||
            (isSeries
              ? effectiveItem.folder_paths !== undefined
              : effectiveItem.versions !== undefined)) &&
            (enrichedItemLoading ? (
              <div className="text-muted-foreground bg-muted/30 shrink-0 rounded-lg border px-3 py-2 text-sm">
                Loading local media…
              </div>
            ) : isSeries ? (
              <FolderPathsList paths={effectiveItem.folder_paths ?? []} />
            ) : (
              <MediaLocations
                title="Local media"
                versions={effectiveItem.versions ?? []}
                className="shrink-0"
                compact
                emptyMessage="No file paths are available for this item."
              />
            ))}

          {/* Search inputs */}
          <div className="grid shrink-0 grid-cols-2 gap-3">
            <div className="col-span-2">
              <Label htmlFor="match-title">Title</Label>
              <Input
                id="match-title"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="Title"
              />
            </div>
            <div>
              <Label htmlFor="match-year">Year</Label>
              <Input
                id="match-year"
                value={year}
                onChange={(e) => setYear(e.target.value)}
                placeholder="Year"
                type="number"
              />
            </div>
            {showVideoExternalIds ? (
              <>
                <div>
                  <Label htmlFor="match-imdb">IMDb ID</Label>
                  <Input
                    id="match-imdb"
                    value={imdbId}
                    onChange={(e) => setImdbId(e.target.value)}
                    placeholder="tt1234567"
                  />
                </div>
                <div>
                  <Label htmlFor="match-tmdb">TMDB ID</Label>
                  <Input
                    id="match-tmdb"
                    value={tmdbId}
                    onChange={(e) => setTmdbId(e.target.value)}
                    placeholder="12345"
                  />
                </div>
                <div>
                  <Label htmlFor="match-tvdb">TVDB ID</Label>
                  <Input
                    id="match-tvdb"
                    value={tvdbId}
                    onChange={(e) => setTvdbId(e.target.value)}
                    placeholder="12345"
                  />
                </div>
              </>
            ) : (
              <div className="col-span-2 space-y-2">
                <Label>Provider IDs</Label>
                {providerIdInputs.map((entry, index) => (
                  <div
                    key={entry.id}
                    className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)_2rem] gap-2"
                  >
                    <Input
                      value={entry.provider}
                      onChange={(e) => updateProviderIdInput(index, "provider", e.target.value)}
                      placeholder="isbn"
                      aria-label="Provider"
                    />
                    <Input
                      value={entry.value}
                      onChange={(e) => updateProviderIdInput(index, "value", e.target.value)}
                      placeholder="978..."
                      aria-label="Provider ID"
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-9 w-9"
                      onClick={() => removeProviderIdInput(index)}
                      disabled={providerIdInputs.length === 1 && !entry.provider && !entry.value}
                      aria-label="Remove provider ID"
                      title="Remove provider ID"
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="h-9 w-9"
                  onClick={addProviderIdInput}
                  aria-label="Add provider ID"
                  title="Add provider ID"
                >
                  <Plus className="h-4 w-4" />
                </Button>
              </div>
            )}
          </div>

          <Button
            onClick={handleSearch}
            disabled={searchMutation.isPending}
            className="w-full shrink-0 gap-2"
          >
            <Search className={cn("h-4 w-4", searchMutation.isPending && "animate-spin")} />
            Search
          </Button>

          {/* Candidate list */}
          {candidates.length > 0 && (
            <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2 overflow-hidden">
              <Label className="shrink-0">Results</Label>
              <TooltipProvider delayDuration={150}>
                <div className="overlay-scroll min-h-0 flex-1 space-y-1 overflow-y-auto overscroll-contain pr-1 pb-1">
                  {candidates.map((candidate, index) => {
                    const candidateKey = Object.entries(candidate.provider_ids)
                      .map(([k, v]) => `${k}-${v}`)
                      .join("_");
                    return (
                      <button
                        key={`${candidateKey}-${index}`}
                        type="button"
                        className={cn(
                          "flex w-full min-w-0 items-start gap-3 rounded-lg border p-3 text-left transition-colors",
                          selectedCandidate === candidate
                            ? "border-primary bg-primary/5"
                            : "border-border hover:bg-muted/50",
                        )}
                        onClick={() => setSelectedCandidate(candidate)}
                        data-testid="match-candidate"
                      >
                        {candidate.image_url ? (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <img
                                src={candidate.image_url}
                                alt=""
                                className="h-24 w-16 shrink-0 cursor-zoom-in rounded object-cover"
                              />
                            </TooltipTrigger>
                            <TooltipContent
                              side="right"
                              className="border-border/60 overflow-hidden border bg-transparent p-0 shadow-xl"
                            >
                              <img
                                src={candidate.image_url}
                                alt={candidate.title}
                                className="h-72 w-48 rounded-md object-cover"
                              />
                            </TooltipContent>
                          </Tooltip>
                        ) : (
                          <div className="bg-muted h-24 w-16 shrink-0 rounded" />
                        )}
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium">{candidate.title}</div>
                          <div className="text-muted-foreground text-xs">
                            {candidate.year ? candidate.year : ""}
                          </div>
                          <div className="mt-1 flex min-w-0 flex-wrap gap-1">
                            {candidate.sources.map((source) => (
                              <Badge key={source} variant="outline" className="text-[10px]">
                                {source}
                              </Badge>
                            ))}
                            {candidate.sources.length > 1 && (
                              <Badge variant="secondary" className="text-[10px]">
                                {candidate.sources.length} sources agree
                              </Badge>
                            )}
                          </div>
                        </div>
                      </button>
                    );
                  })}
                </div>
              </TooltipProvider>
            </div>
          )}

          {searchMutation.isSuccess && candidates.length === 0 && (
            <p className="text-muted-foreground text-center text-sm">No candidates found.</p>
          )}
        </div>

        {/* Apply button — pinned below scroll area */}
        {selectedCandidate && (
          <div className="border-border/50 shrink-0 border-t pt-4">
            <Button
              onClick={handleApply}
              disabled={applyMutation.isPending}
              className="w-full"
              data-testid="apply-match"
            >
              {applyMutation.isPending ? "Applying..." : "Apply Match"}
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function getFolderDisplayName(path: string): string {
  const trimmed = path.trim().replace(/[\\/]+$/, "");
  if (!trimmed) return path;

  const segments = trimmed.split(/[\\/]/).filter(Boolean);
  return segments[segments.length - 1] || trimmed;
}

function computeRootPath(paths: string[]): string {
  if (paths.length === 0) return "";

  const parentSegments = paths.map((path) => {
    const trimmed = path.trim().replace(/[\\/]+$/, "");
    return trimmed.split(/[\\/]/).filter(Boolean).slice(0, -1);
  });
  const first = parentSegments[0] ?? [];
  const minLen = Math.min(...parentSegments.map((segments) => segments.length));

  let sharedLength = 0;
  for (let i = 0; i < minLen; i += 1) {
    if (parentSegments.every((segments) => segments[i] === first[i])) {
      sharedLength = i + 1;
      continue;
    }
    break;
  }

  if (sharedLength === 0) return "";
  return `/${first.slice(0, sharedLength).join("/")}`;
}

function FolderPathsList({ paths }: { paths: string[] }) {
  const folderData = useMemo(() => {
    return {
      folders: paths.map((fullPath) => ({
        fullPath,
        displayName: getFolderDisplayName(fullPath),
      })),
      rootPath: computeRootPath(paths),
    };
  }, [paths]);

  if (paths.length === 0) {
    return (
      <section className="shrink-0 space-y-3">
        <h2 className="text-base font-semibold tracking-tight">Local media</h2>
        <div className="text-muted-foreground bg-muted/30 rounded-lg border px-3 py-2 text-sm">
          No folder paths are available for this item.
        </div>
      </section>
    );
  }

  return (
    <section className="shrink-0 space-y-2">
      <h2 className="text-base font-semibold tracking-tight">Local media</h2>
      <div className="bg-background/70 rounded-lg border">
        {folderData.rootPath ? (
          <div className="border-b px-3 py-2">
            <div className="text-muted-foreground mb-1 text-[11px] font-medium tracking-[0.08em] uppercase">
              Root path
            </div>
            <div className="flex items-center gap-2">
              <span
                className="text-muted-foreground min-w-0 flex-1 truncate font-mono text-xs"
                title={folderData.rootPath}
              >
                {folderData.rootPath}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-foreground h-6 w-6 shrink-0"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(folderData.rootPath);
                    toast.success("Copied root path");
                  } catch {
                    toast.error("Failed to copy path");
                  }
                }}
                title="Copy root path"
                aria-label={`Copy root path ${folderData.rootPath}`}
              >
                <Copy className="h-3 w-3" />
              </Button>
            </div>
          </div>
        ) : null}
        <div className="divide-border/50 divide-y">
          {folderData.folders.map(({ fullPath, displayName }) => (
            <div key={fullPath} className="group flex items-center gap-2 px-3 py-2">
              <Folder className="text-muted-foreground h-3.5 w-3.5 shrink-0" />
              <span
                className="min-w-0 flex-1 truncate font-mono text-xs font-medium"
                title={fullPath}
              >
                {displayName}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-foreground h-6 w-6 shrink-0 opacity-0 transition-opacity group-hover:opacity-100"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(fullPath);
                    toast.success("Copied folder path");
                  } catch {
                    toast.error("Failed to copy path");
                  }
                }}
                title="Copy full path"
                aria-label={`Copy folder path ${fullPath}`}
              >
                <Copy className="h-3 w-3" />
              </Button>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
