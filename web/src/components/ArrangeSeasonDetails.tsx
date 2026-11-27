// The arranger's panel for one season: title, description and poster.
import { useEffect, useRef, useState, type CSSProperties } from "react";
import { toast } from "sonner";
import { ImagePlus, Info, Loader2, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { useUploadCustomSeasonPoster } from "@/hooks/queries/episodeOrder";
import { useT } from "@/i18n";
import { seasonDisplayName } from "@/pages/ItemDetail/itemDetailLayout";

import {
  type ArrangedSeason,
  type CustomSeasonPosterUpload,
  type ProviderSeason,
} from "@/lib/arrangeSeasons";

export function SeasonDetails({
  season,
  number,
  provider,
  inherited,
  contentId,
  onTitle,
  onOverview,
  onPoster,
}: {
  season: ArrangedSeason;
  number: number;
  provider: string;
  inherited?: ProviderSeason;
  contentId: string;
  onTitle: (title: string) => void;
  onOverview: (overview: string) => void;
  onPoster: (poster: CustomSeasonPosterUpload | undefined) => void;
}) {
  const t = useT();
  const [overviewKey, setOverviewKey] = useState(0);
  const from = t("web.item.arrangeSeasons.fromProvider", { provider, number });
  const input = useRef<HTMLInputElement>(null);
  // The title field is uncontrolled; a rename in the sidebar updates it in
  // place (remounting it would move focus out of the dialog's focus trap).
  const titleInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const field = titleInput.current;
    if (field && document.activeElement !== field) field.value = season.title;
  }, [season.title]);
  const upload = useUploadCustomSeasonPoster(contentId);
  const poster = season.poster?.url ?? inherited?.posterUrl;
  return (
    <div className="surface-panel mb-4 flex flex-col gap-4 rounded-2xl p-4 sm:flex-row">
      <div className="shrink-0">
        <div className="bg-surface border-border relative aspect-[2/3] w-32 overflow-hidden rounded-xl border">
          {poster ? (
            <img src={poster} alt="" className="h-full w-full object-cover" />
          ) : (
            <div className="text-muted-foreground flex h-full items-center justify-center p-3 text-center text-xs font-medium">
              {season.title || inherited?.title || seasonDisplayName(number)}
            </div>
          )}
          <span className="absolute start-1.5 bottom-1.5 rounded bg-black/70 px-1.5 py-0.5 text-[0.625rem] font-medium text-white/85">
            {season.poster
              ? t("web.item.arrangeSeasons.custom")
              : inherited?.posterUrl
                ? from
                : t("web.item.arrangeSeasons.posterNone")}
          </span>
          {upload.isPending && (
            <span className="absolute inset-0 grid place-items-center bg-black/50">
              <Loader2 className="size-5 animate-spin text-white" aria-hidden="true" />
            </span>
          )}
        </div>
        <input
          ref={input}
          type="file"
          accept="image/jpeg,image/png,image/webp"
          className="hidden"
          onChange={(e) => {
            const file = e.target.files?.[0];
            e.target.value = "";
            if (!file) return;
            upload.mutate(file, {
              onSuccess: (uploaded) => {
                onPoster({ ref: uploaded.poster, url: uploaded.poster_url });
                toast.success(t("web.item.arrangeSeasons.posterAdded"));
              },
              onError: () => toast.error(t("web.item.arrangeSeasons.posterFailed")),
            });
          }}
        />
        <div className="mt-2 flex w-32 flex-col gap-1.5">
          <Button
            size="sm"
            variant="outline"
            disabled={upload.isPending}
            onClick={() => input.current?.click()}
          >
            <ImagePlus className="size-3.5" aria-hidden="true" />
            {season.poster
              ? t("web.item.arrangeSeasons.posterReplace")
              : t("web.item.arrangeSeasons.posterUpload")}
          </Button>
          {season.poster && (
            <Button size="sm" variant="ghost" onClick={() => onPoster(undefined)}>
              <RotateCcw className="size-3.5" aria-hidden="true" />
              {inherited?.posterUrl
                ? t("web.item.arrangeSeasons.useProvider", { provider })
                : t("web.item.arrangeSeasons.posterRemove")}
            </Button>
          )}
        </div>
      </div>
      <div className="min-w-0 flex-1 space-y-3">
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <Label
              htmlFor={`season-title-${season.id}`}
              className="text-muted-foreground text-[0.75rem] font-medium"
            >
              {t("web.item.arrangeSeasons.titleLabel")}
            </Label>
            <span className="text-muted-foreground text-[0.6875rem]">
              {season.title ? t("web.item.arrangeSeasons.custom") : from}
            </span>
          </div>
          <Input
            ref={titleInput}
            id={`season-title-${season.id}`}
            defaultValue={season.title}
            placeholder={inherited?.title || seasonDisplayName(number)}
            onBlur={(e) => onTitle(e.target.value.trim())}
            onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
          />
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <Label
              htmlFor={`season-overview-${season.id}`}
              className="text-muted-foreground text-[0.75rem] font-medium"
            >
              {t("web.item.arrangeSeasons.descriptionLabel")}
            </Label>
            <span className="text-muted-foreground flex items-center gap-2 text-[0.6875rem]">
              {season.overview
                ? t("web.item.arrangeSeasons.custom")
                : inherited?.overview
                  ? from
                  : t("web.item.arrangeSeasons.none")}
              {season.overview && (
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => {
                    onOverview("");
                    setOverviewKey((k) => k + 1);
                  }}
                >
                  <RotateCcw className="size-3" aria-hidden="true" />
                  {inherited?.overview
                    ? t("web.item.arrangeSeasons.useProvider", { provider })
                    : t("web.item.arrangeSeasons.clear")}
                </Button>
              )}
            </span>
          </div>
          <Textarea
            id={`season-overview-${season.id}`}
            key={overviewKey}
            defaultValue={season.overview || inherited?.overview || ""}
            placeholder={t("web.item.arrangeSeasons.descriptionPlaceholder")}
            rows={4}
            style={{ fieldSizing: "content" } as CSSProperties}
            className="max-h-64 min-h-24 text-sm leading-relaxed"
            onBlur={(e) => {
              const text = e.target.value.trim();
              const providerText = (inherited?.overview ?? "").trim();
              onOverview(text === providerText ? "" : text);
            }}
          />
        </div>
        <p className="text-muted-foreground/80 flex gap-1.5 text-xs leading-relaxed">
          <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          {t("web.item.arrangeSeasons.metaNoteSeasons", { provider })}
        </p>
      </div>
    </div>
  );
}
