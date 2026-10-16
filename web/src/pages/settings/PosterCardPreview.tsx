import { Eye, Heart } from "lucide-react";

import CardOverlays from "@/components/overlays/CardOverlays";
import { RequestDownloadBar } from "@/components/overlays/RequestDownloadBar";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useUICustomization } from "@/hooks/useUICustomization";
import {
  showsFavoriteQuickAction,
  showsWatchedQuickAction,
  type CardQuickActionMode,
} from "@/lib/cardQuickActions";
import {
  buildDefaultPrefs,
  OVERLAY_PREVIEW_SAMPLES,
  OVERLAY_PREVIEW_VARIANTS,
  requestDownloadBarPercent,
  type CardOverlayPrefs,
  type OverlayPreviewVariant,
} from "@/lib/overlays";
import type { CardPresentation } from "@/lib/uiCustomization";
import { cn } from "@/lib/utils";

/** Made-up titles: the preview must never look like someone's real library. */
const SAMPLES: Record<OverlayPreviewVariant, { title: string; meta: string; art: string }> = {
  movie: {
    title: "Northern Lights",
    meta: "2024 · 2h 28m",
    art: "from-sky-700 via-indigo-800 to-slate-950",
  },
  show: {
    title: "The Long Coast",
    meta: "2024 · Series",
    art: "from-amber-600 via-rose-800 to-slate-950",
  },
  requested: {
    title: "Glass Harbor",
    meta: "2026 · Requested",
    art: "from-emerald-700 via-teal-900 to-slate-950",
  },
};

const SHORTCUT_TILE =
  "border-border/20 bg-background/80 flex size-6 items-center justify-center rounded-md border shadow-sm";

/**
 * How wide a sample poster is at each size. Every sample shows at every size,
 * wrapping when the row runs out, so the request sample and the show-only
 * badges can be previewed whatever the size.
 */
const POSTER_WIDTH: Record<CardPresentation["poster_size"], string> = {
  compact: "w-[5.25rem]",
  standard: "w-[6.5rem]",
  large: "w-32",
};

interface PosterCardPreviewViewProps {
  presentation: CardPresentation;
  /** Null while badges are off. */
  overlayPrefs: CardOverlayPrefs | null;
  quickActionMode: CardQuickActionMode;
  /** Samples this server can show; the request sample needs request support. */
  variants: readonly OverlayPreviewVariant[];
  /** Show the shortcut buttons without hovering, right after they change. */
  revealShortcuts?: boolean;
}

export function PosterCardPreviewView({
  presentation,
  overlayPrefs,
  quickActionMode,
  variants,
  revealShortcuts = false,
}: PosterCardPreviewViewProps) {
  const width = POSTER_WIDTH[presentation.poster_size];
  const hasShortcuts = quickActionMode !== "none";

  return (
    <figure className="surface-panel-subtle rounded-2xl border p-4" aria-label="Preview">
      <figcaption className="mb-3 flex items-baseline justify-between gap-3">
        <span className="text-sm font-semibold">Preview</span>
        <span className="text-muted-foreground text-[13px]">Updates as you change things</span>
      </figcaption>
      <div className="flex min-h-56 flex-wrap items-start justify-center gap-2">
        {variants.map((variant) => {
          const sample = SAMPLES[variant];
          const data = OVERLAY_PREVIEW_SAMPLES[variant];
          const downloadPercent = overlayPrefs
            ? requestDownloadBarPercent(data, overlayPrefs)
            : null;
          return (
            // Holding a poster on a touch screen keeps it :active, which shows
            // its shortcuts as the hint below says. The empty touch handler is
            // what lets iOS Safari apply :active at all.
            <div
              key={variant}
              onTouchStart={() => {}}
              className={cn(
                "group/preview shrink-0 transition-[width] duration-300 select-none [-webkit-touch-callout:none]",
                width,
              )}
            >
              <div
                className={cn(
                  "relative aspect-[2/3] overflow-hidden rounded-lg bg-gradient-to-br shadow-md",
                  sample.art,
                )}
              >
                {downloadPercent !== null ? <RequestDownloadBar percent={downloadPercent} /> : null}
                {overlayPrefs ? (
                  <CardOverlays
                    data={data}
                    prefs={overlayPrefs}
                    hasProgressBar={downloadPercent !== null}
                  />
                ) : null}
                {hasShortcuts ? (
                  <div
                    aria-hidden="true"
                    className={cn(
                      "absolute bottom-1.5 left-1.5 z-20 flex gap-1 transition-opacity duration-200",
                      revealShortcuts
                        ? "opacity-100"
                        : "opacity-0 group-hover/preview:opacity-100 group-active/preview:opacity-100",
                    )}
                  >
                    {showsWatchedQuickAction(quickActionMode) ? (
                      <span className={cn(SHORTCUT_TILE, "text-foreground")}>
                        <Eye className="size-3.5" />
                      </span>
                    ) : null}
                    {showsFavoriteQuickAction(quickActionMode) ? (
                      <span className={cn(SHORTCUT_TILE, "text-rose-400")}>
                        <Heart className="size-3.5 fill-current" />
                      </span>
                    ) : null}
                  </div>
                ) : null}
              </div>
              {presentation.caption !== "artwork" ? (
                <div className="mt-1.5 min-w-0">
                  <p className="truncate text-xs font-medium">{sample.title}</p>
                  {presentation.caption === "title_metadata" ? (
                    <p className="text-muted-foreground truncate text-xs">{sample.meta}</p>
                  ) : null}
                </div>
              ) : null}
            </div>
          );
        })}
      </div>
      {hasShortcuts ? (
        <p className="text-muted-foreground mt-3 text-[13px] leading-relaxed">
          Point at a poster to see its shortcuts. On touch screens, press and hold instead.
        </p>
      ) : null}
    </figure>
  );
}

/**
 * The live preview for the Navigation & Cards page. It reads the same settings
 * the controls write, so every change shows here at once.
 */
export function PosterCardPreview({ revealShortcuts }: { revealShortcuts: boolean }) {
  const { cardPresentation } = useUICustomization();
  const { prefs, overlaysEnabled, quickActionPreference, quickActionsEnabled, isOverlaySupported } =
    useOverlayPrefs();
  // The request sample only shows a badge this server lets the profile keep.
  const variants = isOverlaySupported("request_status")
    ? OVERLAY_PREVIEW_VARIANTS
    : OVERLAY_PREVIEW_VARIANTS.filter((variant) => variant !== "requested");

  return (
    <PosterCardPreviewView
      presentation={cardPresentation}
      overlayPrefs={overlaysEnabled ? (prefs ?? buildDefaultPrefs()) : null}
      quickActionMode={quickActionsEnabled ? quickActionPreference : "none"}
      variants={variants}
      revealShortcuts={revealShortcuts}
    />
  );
}
