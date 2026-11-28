// The episode order dialog's Seasons and Absolute layouts: the show as the
// chosen order lays it out, read-only, with the file Silo links to each
// episode. Moving episodes or renaming seasons happens in the Custom layout.
import { useCallback, useMemo, useRef, useState, type ReactNode } from "react";
import { FileSearch, Info, Loader2, TriangleAlert } from "lucide-react";
import { EpisodeSummary, VirtualEpisodeList } from "@/components/ArrangeSeasonsRows";
import { Button } from "@/components/ui/button";
import {
  useCustomSeasons,
  type CustomSeasonEpisode,
  type CustomSeasons,
  type EpisodeOrderChoice,
  type EpisodeOrderPreview,
} from "@/hooks/queries/episodeOrder";
import { useT } from "@/i18n";
import { joinList } from "@/lib/appLocale";
import {
  asProvider,
  fileOrderLabel,
  providerLabel,
  suggestedFiles,
  type Provider,
} from "@/lib/episodeOrder";
import { cn } from "@/lib/utils";
import { seasonDisplayName } from "@/pages/ItemDetail/itemDetailLayout";

/** A season as the read-only layouts show it. */
interface ShownSeason {
  number: number;
  title: string;
  overview: string;
  posterUrl?: string;
  episodes: CustomSeasonEpisode[];
}

/**
 * The order's seasons, or for the Absolute layout its regular episodes as one
 * season numbered from 1; Specials last.
 */
function shownSeasons(data: CustomSeasons, absolute: boolean): ShownSeason[] {
  const shown = data.seasons.map((s) => ({
    number: s.number,
    title: s.provider_title,
    overview: s.provider_overview,
    posterUrl: s.provider_poster_url,
    episodes: s.episodes,
  }));
  if (!absolute) return shown;
  const regular = shown.filter((s) => s.number !== 0);
  return [
    {
      number: 1,
      title: "",
      overview: "",
      posterUrl: regular[0]?.posterUrl,
      episodes: regular.flatMap((s) => s.episodes),
    },
    ...shown.filter((s) => s.number === 0),
  ];
}

export function EpisodeOrderSeasonsView({
  contentId,
  choice,
  structure,
  files,
  preview,
  onReadFilesAs,
  onArrange,
}: {
  contentId: string;
  /** The Seasons or Absolute choice being shown. */
  choice: EpisodeOrderChoice;
  /** The provider the chosen order belongs to. */
  structure: Provider;
  /** The provider whose numbering the file names follow. */
  files: Provider;
  preview: EpisodeOrderPreview | undefined;
  onReadFilesAs: (provider: Provider) => void;
  onArrange: () => void;
}) {
  const t = useT();
  // The order's seasons with stills, posters and files, as the arranger
  // would start from them.
  const query = useCustomSeasons(contentId, true, { ...choice, layout: "custom" });
  const data = query.data;
  const absolute = choice.layout === "absolute";
  const seasons = useMemo(() => (data ? shownSeasons(data, absolute) : []), [data, absolute]);
  const [seasonNumber, setSeasonNumber] = useState<number | null>(null);
  // The episode list scrolls in the details pane on wide screens and with
  // the whole body on narrow ones; the virtual list follows whichever.
  const paneRef = useRef<HTMLElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const scrollElement = useCallback(() => {
    const pane = paneRef.current;
    return pane && getComputedStyle(pane).overflowY !== "visible" ? pane : bodyRef.current;
  }, []);
  const regular = seasons.filter((s) => s.number !== 0);
  const specials = seasons.find((s) => s.number === 0);
  const shown =
    seasons.find((s) => s.number === seasonNumber) ??
    seasons.find((s) => s.episodes.some((e) => e.file)) ??
    seasons[0];
  const missingFiles = (preview?.file_count ?? 0) - (preview?.matched_files ?? 0);
  const suggestion = suggestedFiles(preview, files, structure);
  const unmatched = useMemo(() => {
    const names = joinList((preview?.unmatched_files ?? []).slice(0, 3));
    return missingFiles > 3
      ? t("web.item.episodeOrder.preview.moreFiles", { names, count: missingFiles - 3 })
      : names;
  }, [preview, missingFiles, t]);
  const provider = providerLabel(data?.structure_provider || structure);

  if (!data) {
    return (
      <div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 p-10 text-sm">
        {query.isError ? (
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
  if (data.structure_provider === "") {
    return (
      <div className="p-5">
        <Notice tone="info" title={t("web.item.episodeOrder.preview.empty")} />
      </div>
    );
  }

  const nameOf = (s: ShownSeason) => {
    const plain = seasonDisplayName(s.number);
    const named = seasonDisplayName(s.number, s.title);
    return s.number === 0 || named === plain
      ? plain
      : t("web.item.arrangeSeasons.seasonTitled", { number: s.number, title: named });
  };
  const seasonButton = (s: ShownSeason) => {
    const onDisk = s.episodes.filter((e) => e.file).length;
    const meta = [t("web.item.arrangeSeasons.episodeCount", { count: s.episodes.length })];
    if (onDisk > 0) meta.push(t("web.item.episodeOrder.preview.onDisk", { count: onDisk }));
    return (
      <li key={s.number}>
        <button
          type="button"
          onClick={() => setSeasonNumber(s.number)}
          aria-current={s.number === shown?.number}
          className={cn(
            "flex w-full items-center gap-2.5 rounded-lg border border-transparent px-2.5 py-1.5 text-start",
            s.number === shown?.number ? "bg-primary/10 border-primary/30" : "hover:bg-accent/50",
          )}
        >
          {s.number !== 0 && (
            <span className="bg-surface border-border/60 h-12 w-8 shrink-0 overflow-hidden rounded-md border">
              {s.posterUrl && (
                <img
                  src={s.posterUrl}
                  alt=""
                  className="h-full w-full object-cover"
                  loading="lazy"
                />
              )}
            </span>
          )}
          <span className="min-w-0 flex-1">
            <span className="block text-sm leading-snug font-medium text-pretty">{nameOf(s)}</span>
            <span className="text-muted-foreground block text-xs">{meta.join(" · ")}</span>
          </span>
        </button>
      </li>
    );
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="border-border/10 text-muted-foreground flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b px-5 py-2.5 text-xs">
        <span>{t("web.item.arrangeSeasons.seasonCount", { count: regular.length })}</span>
        <span>
          {t("web.item.arrangeSeasons.episodeCount", {
            count: seasons.reduce((n, s) => n + s.episodes.length, 0),
          })}
        </span>
        {preview && preview.file_count > 0 && (
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

      <div
        ref={bodyRef}
        className="flex max-h-[70vh] min-h-0 flex-1 flex-col overflow-y-auto md:max-h-none md:flex-row md:overflow-hidden"
      >
        <aside className="border-border/10 min-h-0 shrink-0 p-3 md:w-[420px] md:overflow-y-auto md:border-e">
          <h2 className="px-1.5 pb-2 text-sm font-semibold">
            {t("web.item.arrangeSeasons.seasons")}
          </h2>
          <ul className="space-y-1">{regular.map(seasonButton)}</ul>
          {specials && (
            <ul className="border-border/60 mt-2 border-t pt-2">{seasonButton(specials)}</ul>
          )}
        </aside>

        <section ref={paneRef} className="min-h-0 min-w-0 flex-1 p-5 md:overflow-y-auto">
          {data.structure_provider !== structure && (
            <Notice
              tone="info"
              title={t("web.item.episodeOrder.preview.fallback", {
                provider: providerLabel(structure),
                other: providerLabel(asProvider(data.structure_provider)),
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
              <div className="mb-3 flex flex-wrap items-center gap-3">
                <h2 className="text-xl font-semibold tracking-tight">{nameOf(shown)}</h2>
                <span className="text-muted-foreground text-sm">
                  {t("web.item.arrangeSeasons.episodeCount", { count: shown.episodes.length })}
                </span>
              </div>
              {shown.number !== 0 && !absolute && <SeasonCard season={shown} provider={provider} />}
              <VirtualEpisodeList
                // Each season scrolls from its top.
                key={shown.number}
                keys={shown.episodes.map((e) => e.key)}
                scrollElement={scrollElement}
                renderRow={(_, i) => {
                  const episode = shown.episodes[i];
                  return episode ? (
                    <div className="border-border/40 flex items-center gap-3 border-b px-3 py-2">
                      <EpisodeSummary episode={episode} provider={provider} number={i + 1} />
                    </div>
                  ) : null;
                }}
              />
            </>
          )}
        </section>
      </div>
    </div>
  );
}

/** A season's poster, title and description as the provider has them. */
function SeasonCard({ season, provider }: { season: ShownSeason; provider: string }) {
  const t = useT();
  return (
    <div className="surface-panel mb-4 flex gap-4 rounded-2xl p-4">
      <div className="bg-surface border-border relative aspect-[2/3] w-24 shrink-0 overflow-hidden rounded-xl border">
        {season.posterUrl ? (
          <img src={season.posterUrl} alt="" className="h-full w-full object-cover" />
        ) : (
          <div className="text-muted-foreground flex h-full items-center justify-center p-2 text-center text-xs font-medium">
            {t("web.item.arrangeSeasons.posterNone")}
          </div>
        )}
      </div>
      <div className="min-w-0 flex-1 space-y-1.5">
        <p className="text-muted-foreground text-[0.6875rem]">
          {t("web.item.arrangeSeasons.fromProvider", { provider, number: season.number })}
        </p>
        {season.overview && (
          <p className="text-muted-foreground text-sm leading-relaxed whitespace-pre-line">
            {season.overview}
          </p>
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
