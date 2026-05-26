import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, Download, Ear, Loader2, Search } from "lucide-react";

import type { FileVersion, SubtitleResult } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import {
  searchSubtitles,
  detectSubtitleLanguage,
  useDownloadSubtitle,
  useDownloadedSubtitles,
  useUploadSubtitle,
} from "@/hooks/queries/subtitles";
import { cn } from "@/lib/utils";
import { LANGUAGES, getLanguageName } from "@/player/utils/languageNames";
import { SubtitleUploadForm } from "@/components/subtitles/SubtitleUploadForm";
import { buildQualitySummary } from "./VersionFlyout";

interface SubtitleSearchDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  version: FileVersion | null;
  title: string;
}

const providerInfo: Record<string, { abbr: string; className: string }> = {
  opensubtitles: { abbr: "OS", className: "bg-amber-500/15 text-amber-700 dark:text-amber-300" },
  subdl: { abbr: "SDL", className: "bg-sky-500/15 text-sky-700 dark:text-sky-300" },
  subsource: { abbr: "SS", className: "bg-rose-500/15 text-rose-700 dark:text-rose-300" },
  upload: { abbr: "UP", className: "bg-violet-500/15 text-violet-700 dark:text-violet-300" },
};

function scoreTone(score: number): { text: string; ring: string; bg: string } {
  if (score >= 70) {
    return {
      text: "text-emerald-600 dark:text-emerald-300",
      ring: "ring-emerald-500/30 dark:ring-emerald-400/30",
      bg: "bg-emerald-500/10 dark:bg-emerald-400/10",
    };
  }
  if (score >= 40) {
    return {
      text: "text-amber-600 dark:text-amber-300",
      ring: "ring-amber-500/30 dark:ring-amber-400/30",
      bg: "bg-amber-500/10 dark:bg-amber-400/10",
    };
  }
  return {
    text: "text-rose-600 dark:text-rose-300",
    ring: "ring-rose-500/30 dark:ring-rose-400/30",
    bg: "bg-rose-500/10 dark:bg-rose-400/10",
  };
}

function splitReleaseNames(raw: string): string[] {
  return raw
    .split(/[\r\n]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export default function SubtitleSearchDialog({
  open,
  onOpenChange,
  version,
  title,
}: SubtitleSearchDialogProps) {
  const downloadSubtitleMutation = useDownloadSubtitle();
  const uploadSubtitleMutation = useUploadSubtitle();
  const downloadedQuery = useDownloadedSubtitles(open ? version?.file_id : undefined);
  const searchAbortRef = useRef<AbortController | null>(null);

  const [selectedLanguage, setSelectedLanguage] = useState("en");
  const [results, setResults] = useState<SubtitleResult[]>([]);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [searchError, setSearchError] = useState<string | null>(null);
  const [searching, setSearching] = useState(false);
  const [hasSearched, setHasSearched] = useState(false);
  const [downloadingKey, setDownloadingKey] = useState<string | null>(null);
  const [expandedKeys, setExpandedKeys] = useState<Set<string>>(() => new Set());

  // Reset state when dialog opens/closes or version changes.
  useEffect(() => {
    if (!open) {
      searchAbortRef.current?.abort();
      searchAbortRef.current = null;
      setSearching(false);
      return;
    }

    setSelectedLanguage("en");
    setResults([]);
    setWarnings([]);
    setSearchError(null);
    setHasSearched(false);
    setExpandedKeys(new Set());
  }, [open, version?.file_id]);

  const toggleExpanded = useCallback((key: string) => {
    setExpandedKeys((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const parsedResults = useMemo(
    () =>
      results.map((result) => ({
        result,
        key: `${result.provider}:${result.id}`,
        names: splitReleaseNames(result.release_name),
      })),
    [results],
  );

  const handleSearch = useCallback(async () => {
    if (!version) return;

    searchAbortRef.current?.abort();
    const controller = new AbortController();
    searchAbortRef.current = controller;

    setSearching(true);
    setHasSearched(true);
    setSearchError(null);
    setWarnings([]);

    try {
      const response = await searchSubtitles(
        {
          media_file_id: version.file_id,
          languages: [selectedLanguage],
        },
        { signal: controller.signal },
      );

      if (controller.signal.aborted) return;

      setResults(response.results ?? []);
      setWarnings(response.warnings ?? []);
    } catch (err) {
      if (controller.signal.aborted) return;
      setResults([]);
      setSearchError(err instanceof Error ? err.message : "Search failed");
    } finally {
      if (!controller.signal.aborted) {
        setSearching(false);
      }
    }
  }, [selectedLanguage, version]);

  const handleDownload = useCallback(
    async (result: SubtitleResult) => {
      if (!version) return;

      const key = `${result.provider}:${result.id}`;
      setDownloadingKey(key);
      setSearchError(null);

      try {
        await downloadSubtitleMutation.mutateAsync({
          media_file_id: version.file_id,
          provider: result.provider,
          subtitle_id: result.id,
          language: result.language,
          release_name: result.release_name,
          format: result.format,
          score: result.score,
          hearing_impaired: result.hearing_impaired,
        });
        await downloadedQuery.refetch();
      } catch {
        // Error is surfaced by the mutation toast.
      } finally {
        setDownloadingKey(null);
      }
    },
    [downloadSubtitleMutation, downloadedQuery, version],
  );

  const handleUpload = useCallback(
    async (input: {
      mediaFileId: number;
      file: File;
      language?: string;
      languageOverride?: boolean;
      hearingImpaired: boolean;
    }) => {
      await uploadSubtitleMutation.mutateAsync({
        media_file_id: input.mediaFileId,
        file: input.file,
        language: input.language,
        language_override: input.languageOverride,
        hearing_impaired: input.hearingImpaired,
      });
    },
    [uploadSubtitleMutation],
  );

  const handleDetectLanguage = useCallback(
    (file: File, fallbackLanguage?: string) => detectSubtitleLanguage(file, fallbackLanguage),
    [],
  );

  const handleUploadSuccess = useCallback(async () => {
    await downloadedQuery.refetch();
  }, [downloadedQuery]);

  const versionLabel = version ? buildQualitySummary(version) : "";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl overflow-hidden sm:max-w-3xl">
        <DialogHeader className="min-w-0">
          <DialogTitle>Add Subtitles</DialogTitle>
          <DialogDescription className="truncate">
            {title}
            {versionLabel ? ` \u00B7 ${versionLabel}` : ""}
          </DialogDescription>
        </DialogHeader>

        <TooltipProvider delayDuration={250}>
          <div className="min-w-0 space-y-4">
            {version && (
              <SubtitleUploadForm
                mediaFileId={version.file_id}
                upload={handleUpload}
                detectLanguage={handleDetectLanguage}
                onSuccess={handleUploadSuccess}
                onError={setSearchError}
                defaultLanguage={selectedLanguage}
              />
            )}

            <div className="space-y-2">
              <p className="text-sm font-medium">Search online</p>
              <div className="flex flex-col gap-2 sm:flex-row">
                <Select value={selectedLanguage} onValueChange={setSelectedLanguage}>
                  <SelectTrigger className="w-full sm:w-[220px]">
                    <SelectValue placeholder="Language" />
                  </SelectTrigger>
                  <SelectContent>
                    {LANGUAGES.map((language) => (
                      <SelectItem key={language.code} value={language.code}>
                        {language.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                <Button onClick={handleSearch} disabled={!version || searching}>
                  {searching ? (
                    <Loader2 className="size-4 animate-spin" />
                  ) : (
                    <Search className="size-4" />
                  )}
                  Search
                </Button>
              </div>
            </div>

            {searchError && (
              <div className="rounded-lg bg-rose-500/10 px-3 py-2 text-sm text-rose-700 dark:text-rose-300">
                {searchError}
              </div>
            )}

            {warnings.length > 0 && (
              <div className="space-y-2">
                {warnings.map((warning) => (
                  <div
                    key={warning}
                    className="rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-300"
                  >
                    {warning}
                  </div>
                ))}
              </div>
            )}

            {parsedResults.length > 0 ? (
              <div className="-mr-1 max-h-[28rem] min-w-0 space-y-2 overflow-x-hidden overflow-y-auto pr-1">
                {parsedResults.map(({ result, key, names }) => {
                  const provider = providerInfo[result.provider] ?? {
                    abbr: result.provider.slice(0, 2).toUpperCase(),
                    className: "bg-muted text-muted-foreground",
                  };
                  const tone = scoreTone(result.score);
                  const score = Math.round(result.score);
                  const isDownloading = downloadingKey === key;
                  const expanded = expandedKeys.has(key);
                  const primary = names[0] ?? result.release_name;
                  const extras = names.slice(1);
                  const hasExtras = extras.length > 0;

                  return (
                    <div
                      key={key}
                      className="border-border/60 bg-accent/20 hover:bg-accent/40 group flex items-stretch gap-3 overflow-hidden rounded-xl border p-3 transition-colors"
                    >
                      <div
                        className={cn(
                          "flex w-12 shrink-0 flex-col items-center justify-center gap-0.5 rounded-lg ring-1 ring-inset",
                          tone.bg,
                          tone.ring,
                        )}
                        aria-label={`Match score ${score}`}
                      >
                        <span
                          className={cn(
                            "text-base leading-none font-semibold tabular-nums",
                            tone.text,
                          )}
                        >
                          {score}
                        </span>
                        <span
                          className={cn(
                            "text-[9px] tracking-[0.12em] uppercase opacity-70",
                            tone.text,
                          )}
                        >
                          Score
                        </span>
                      </div>

                      <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                        <div className="flex flex-wrap items-center gap-1.5">
                          <Badge className={cn("h-5 px-1.5 text-[10px]", provider.className)}>
                            {provider.abbr}
                          </Badge>
                          <Badge variant="outline" className="h-5 px-1.5 text-[10px] uppercase">
                            {result.format}
                          </Badge>
                          <Badge variant="outline" className="h-5 px-1.5 text-[10px]">
                            {getLanguageName(result.language)}
                          </Badge>
                          {result.hearing_impaired && (
                            <Badge variant="outline" className="h-5 gap-1 px-1.5 text-[10px]">
                              <Ear className="size-2.5" /> HI
                            </Badge>
                          )}
                          {result.downloads > 0 && (
                            <span className="text-muted-foreground ml-0.5 text-[11px] tabular-nums">
                              {result.downloads.toLocaleString()}{" "}
                              {result.downloads === 1 ? "download" : "downloads"}
                            </span>
                          )}
                        </div>

                        <div className="min-w-0 space-y-0.5">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <p
                                className="text-foreground hover:text-foreground/90 block cursor-default truncate font-mono text-[13px] leading-snug"
                                tabIndex={0}
                              >
                                {primary}
                              </p>
                            </TooltipTrigger>
                            <TooltipContent
                              side="bottom"
                              align="start"
                              className="font-mono break-all"
                            >
                              {hasExtras ? (
                                <div className="space-y-1.5">
                                  <p className="text-muted-foreground font-sans text-[10px] tracking-[0.12em] uppercase">
                                    {names.length} release name{names.length === 1 ? "" : "s"}
                                  </p>
                                  <ul className="space-y-1 leading-snug">
                                    {names.map((name, i) => (
                                      <li key={i}>{name}</li>
                                    ))}
                                  </ul>
                                </div>
                              ) : (
                                <span className="leading-snug">{primary}</span>
                              )}
                            </TooltipContent>
                          </Tooltip>

                          {hasExtras && expanded && (
                            <ul className="border-border/60 text-muted-foreground space-y-0.5 border-l pl-2 font-mono text-[12px] leading-snug">
                              {extras.map((name, i) => (
                                <li key={i} className="truncate" title={name}>
                                  {name}
                                </li>
                              ))}
                            </ul>
                          )}
                          {hasExtras && (
                            <button
                              type="button"
                              onClick={() => toggleExpanded(key)}
                              className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-[11px] font-medium transition-colors"
                            >
                              <ChevronDown
                                className={cn(
                                  "size-3 transition-transform",
                                  expanded && "rotate-180",
                                )}
                              />
                              {expanded
                                ? "Collapse"
                                : `${extras.length} more variant${extras.length === 1 ? "" : "s"}`}
                            </button>
                          )}
                        </div>
                      </div>

                      <div className="flex shrink-0 items-center">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => handleDownload(result)}
                          disabled={downloadingKey !== null}
                          className="min-w-[7.5rem]"
                        >
                          {isDownloading ? (
                            <Loader2 className="size-4 animate-spin" />
                          ) : (
                            <Download className="size-4" />
                          )}
                          {isDownloading ? "Downloading" : "Download"}
                        </Button>
                      </div>
                    </div>
                  );
                })}
              </div>
            ) : (
              hasSearched &&
              !searching &&
              !searchError && (
                <div className="text-muted-foreground rounded-xl border border-dashed px-4 py-6 text-center text-sm">
                  No subtitles found for this version and language.
                </div>
              )
            )}
          </div>
        </TooltipProvider>
      </DialogContent>
    </Dialog>
  );
}
