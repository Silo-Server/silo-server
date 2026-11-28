// The episode order dialog's Seasons and Absolute layouts: the show as the
// chosen order lays it out, read-only, with the file Silo links to each
// episode. Moving episodes or renaming seasons happens in the Custom layout.
import { useMemo, useState, type ReactNode } from "react";
import { FileSearch, FileVideo, Info, Loader2, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { EpisodeOrderPreview } from "@/hooks/queries/episodeOrder";
import { useT } from "@/i18n";
import { joinList } from "@/lib/appLocale";
import { formatEpisodeCode } from "@/lib/episodeCardLabels";
import {
  asProvider,
  fileOrderLabel,
  providerLabel,
  suggestedFiles,
  type Provider,
} from "@/lib/episodeOrder";
import { cn } from "@/lib/utils";
import { seasonDisplayName } from "@/pages/ItemDetail/itemDetailLayout";

/** Rows shown per season; a longer season says how many more it has. */
const MAX_ROWS = 200;

export function EpisodeOrderSeasonsView({
  structure,
  files,
  preview,
  failed,
  onReadFilesAs,
  onArrange,
}: {
  /** The provider the chosen order belongs to. */
  structure: Provider;
  /** The provider whose numbering the file names follow. */
  files: Provider;
  preview: EpisodeOrderPreview | undefined;
  failed: boolean;
  onReadFilesAs: (provider: Provider) => void;
  onArrange: () => void;
}) {
  const t = useT();
  const [seasonNumber, setSeasonNumber] = useState<number | null>(null);
  const seasons = preview?.seasons ?? [];
  const regular = seasons.filter((s) => s.number !== 0);
  const specials = seasons.find((s) => s.number === 0);
  const shown =
    seasons.find((s) => s.number === seasonNumber) ??
    seasons.find((s) => s.file_count > 0) ??
    seasons[0];
  const missingFiles = (preview?.file_count ?? 0) - (preview?.matched_files ?? 0);
  const suggestion = suggestedFiles(preview, files, structure);
  const unmatched = useMemo(() => {
    const names = joinList((preview?.unmatched_files ?? []).slice(0, 3));
    return missingFiles > 3
      ? t("web.item.episodeOrder.preview.moreFiles", { names, count: missingFiles - 3 })
      : names;
  }, [preview, missingFiles, t]);

  if (!preview) {
    return (
      <div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 p-10 text-sm">
        {failed ? (
          t("web.item.episodeOrder.preview.failed")
        ) : (
          <>
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            {t("web.item.episodeOrder.preview.loading")}
          </>
        )}
      </div>
    );
  }
  if (preview.structure_provider === "") {
    return (
      <div className="p-5">
        <Notice tone="info" title={t("web.item.episodeOrder.preview.empty")} />
      </div>
    );
  }

  const seasonButton = (s: (typeof seasons)[number]) => {
    const name = seasonDisplayName(s.number);
    const titled = seasonDisplayName(s.number, s.title);
    const meta = [t("web.item.episodeOrder.preview.episodes", { count: s.episode_count })];
    if (s.file_count > 0)
      meta.push(t("web.item.episodeOrder.preview.onDisk", { count: s.file_count }));
    return (
      <li key={s.number}>
        <button
          type="button"
          onClick={() => setSeasonNumber(s.number)}
          aria-current={s.number === shown?.number}
          className={cn(
            "flex w-full flex-col rounded-lg border border-transparent px-3 py-2 text-start",
            s.number === shown?.number ? "bg-primary/10 border-primary/30" : "hover:bg-accent/50",
          )}
        >
          <span className="truncate text-sm font-medium">
            {titled === name
              ? name
              : t("web.item.arrangeSeasons.seasonTitled", {
                  number: s.number,
                  title: titled,
                })}
          </span>
          <span className="text-muted-foreground text-xs">{meta.join(" · ")}</span>
        </button>
      </li>
    );
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="border-border/10 text-muted-foreground flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b px-5 py-2.5 text-xs">
        <span>{t("web.item.episodeOrder.preview.seasons", { count: regular.length })}</span>
        <span>{t("web.item.episodeOrder.preview.episodes", { count: preview.episode_count })}</span>
        {preview.file_count > 0 && (
          <span
            className={cn("font-medium", missingFiles === 0 ? "text-green-500" : "text-amber-500")}
          >
            {t("web.item.episodeOrder.preview.files", {
              matched: preview.matched_files,
              total: preview.file_count,
            })}
          </span>
        )}
        <span className="min-w-0 flex-1 basis-60">{t("web.item.episodeOrder.readOnlyHint")}</span>
        <Button variant="outline" size="xs" onClick={onArrange}>
          {t("web.item.episodeOrder.arrange")}
        </Button>
      </div>

      <div className="flex max-h-[60vh] min-h-0 flex-1 flex-col overflow-y-auto md:max-h-none md:flex-row md:overflow-hidden">
        <aside className="border-border/10 min-h-0 shrink-0 p-3 md:w-[320px] md:overflow-y-auto md:border-e">
          <ul className="space-y-1">{regular.map(seasonButton)}</ul>
          {specials && (
            <ul className="border-border/60 mt-2 border-t pt-2">{seasonButton(specials)}</ul>
          )}
        </aside>

        <section className="min-h-0 min-w-0 flex-1 p-5 md:overflow-y-auto">
          {preview.structure_provider !== structure && (
            <Notice
              tone="info"
              title={t("web.item.episodeOrder.preview.fallback", {
                provider: providerLabel(structure),
                other: providerLabel(asProvider(preview.structure_provider)),
              })}
            />
          )}
          {missingFiles > 0 && (
            <Notice
              tone="warn"
              title={t("web.item.episodeOrder.preview.unmatchedTitle", { count: missingFiles })}
              action={
                suggestion
                  ? {
                      label: t("web.item.episodeOrder.preview.readAs", {
                        order: fileOrderLabel(t, suggestion),
                      }),
                      onClick: () => onReadFilesAs(suggestion),
                    }
                  : undefined
              }
            >
              {t("web.item.episodeOrder.preview.unmatchedBody", {
                files: unmatched,
                count: missingFiles,
              })}
            </Notice>
          )}

          {shown && (
            <>
              <div className="mb-3 flex flex-wrap items-baseline gap-3">
                <h2 className="text-xl font-semibold tracking-tight">
                  {seasonDisplayName(shown.number, shown.title)}
                </h2>
                <span className="text-muted-foreground text-sm">
                  {[
                    t("web.item.episodeOrder.preview.episodes", { count: shown.episode_count }),
                    ...(shown.file_count > 0
                      ? [t("web.item.episodeOrder.preview.onDisk", { count: shown.file_count })]
                      : []),
                  ].join(" · ")}
                </span>
              </div>
              <EpisodeTable season={shown} />
            </>
          )}
        </section>
      </div>
    </div>
  );
}

/** The episode's number in the file naming order, or a dash without a file. */
function fileNumber(e: EpisodeOrderPreview["seasons"][number]["episodes"][number]) {
  return e.file_episode > 0 ? formatEpisodeCode(e.file_season, e.file_episode) : "–";
}

function EpisodeTable({ season }: { season: EpisodeOrderPreview["seasons"][number] }) {
  const t = useT();
  return (
    <div className="border-border/10 overflow-hidden rounded-xl border">
      <div className="text-muted-foreground border-border/10 bg-background/40 flex items-center gap-3 border-b px-3 py-2 text-[0.6875rem] font-medium tracking-wide uppercase">
        <span className="w-8 shrink-0 text-end">
          {t("web.item.episodeOrder.preview.colNumber")}
        </span>
        <span className="min-w-0 flex-1">{t("web.item.episodeOrder.preview.colEpisode")}</span>
        <span className="hidden w-[38%] shrink-0 sm:block">
          {t("web.item.episodeOrder.preview.colFile")}
        </span>
        <span className="w-[72px] shrink-0 text-end">
          {t("web.item.episodeOrder.preview.colFileNumber")}
        </span>
      </div>
      <div className="py-1">
        {season.episodes.slice(0, MAX_ROWS).map((e) => (
          <div
            key={`${season.number}-${e.number}`}
            className="hover:bg-accent/40 flex items-center gap-3 px-3 py-1.5 text-[0.8125rem]"
          >
            <span className="text-muted-foreground w-8 shrink-0 text-end font-mono text-xs tabular-nums">
              {e.number}
            </span>
            <span className={cn("min-w-0 flex-1 truncate", !e.file && "text-muted-foreground")}>
              {e.title}
            </span>
            <span className="hidden w-[38%] shrink-0 sm:block">
              {e.file ? (
                <span className="text-muted-foreground flex min-w-0 items-center gap-1.5 font-mono text-[0.6875rem]">
                  <FileVideo className="size-3 shrink-0 text-green-500/80" aria-hidden="true" />
                  <span className="truncate">{e.file}</span>
                </span>
              ) : (
                <span className="text-muted-foreground/60 text-[0.6875rem]">
                  {t("web.item.episodeOrder.preview.notOnDisk")}
                </span>
              )}
            </span>
            <span className="text-muted-foreground/70 w-[72px] shrink-0 text-end font-mono text-[0.6875rem]">
              {fileNumber(e)}
            </span>
          </div>
        ))}
        {season.episodes.length > MAX_ROWS && (
          <div className="text-muted-foreground px-3 py-2 text-xs">
            {t("web.item.episodeOrder.preview.moreEpisodes", {
              count: season.episodes.length - MAX_ROWS,
            })}
          </div>
        )}
      </div>
    </div>
  );
}

// Matches the admin RestartBanner: icon tile, title and explanation, and the
// action as a button on the end (wrapping under the text when narrow).
function Notice({
  title,
  tone,
  action,
  children,
}: {
  title: string;
  tone: "warn" | "info";
  action?: { label: string; onClick: () => void };
  children?: ReactNode;
}) {
  const Icon = tone === "warn" ? TriangleAlert : Info;
  return (
    <div
      role="status"
      className={cn(
        "mb-4 flex flex-wrap items-center gap-3 rounded-2xl border px-4 py-3",
        tone === "warn"
          ? "border-amber-500/30 bg-[linear-gradient(calc(90deg*var(--dir-sign)),color-mix(in_srgb,var(--warning)_16%,transparent),color-mix(in_srgb,var(--warning)_6%,transparent))]"
          : "border-border bg-surface",
      )}
    >
      <span
        className={cn(
          "grid size-7 shrink-0 place-items-center self-start rounded-lg",
          tone === "warn"
            ? "bg-amber-500/15 text-amber-600 dark:text-amber-400"
            : "bg-muted text-muted-foreground",
        )}
      >
        <Icon className="size-3.5" aria-hidden="true" />
      </span>
      <div className="min-w-0 flex-1 basis-64">
        <p className="text-[0.8125rem] font-medium">{title}</p>
        {children && (
          <p className="text-muted-foreground text-[0.71875rem] leading-relaxed">{children}</p>
        )}
      </div>
      {action && (
        <Button size="sm" className="shrink-0" onClick={action.onClick}>
          <FileSearch className="size-3.5" aria-hidden="true" />
          {action.label}
        </Button>
      )}
    </div>
  );
}
