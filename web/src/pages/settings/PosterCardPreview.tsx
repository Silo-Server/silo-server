import { Eye, Heart } from "lucide-react";
import type { CSSProperties } from "react";

import CardOverlays from "@/components/overlays/CardOverlays";
import CardWatchStatus from "@/components/overlays/CardWatchStatus";
import { RequestDownloadBar } from "@/components/overlays/RequestDownloadBar";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useT, type MessageKey } from "@/i18n";
import { useUICustomization } from "@/hooks/useUICustomization";
import {
  showsFavoriteQuickAction,
  showsWatchedQuickAction,
  type CardQuickActionMode,
} from "@/lib/cardQuickActions";
import {
  OVERLAY_PREVIEW_SAMPLES,
  OVERLAY_PREVIEW_VARIANTS,
  OVERLAY_PREVIEW_WATCH_STATES,
  requestDownloadBarPercent,
  type CardOverlayPrefs,
  type OverlayPreviewVariant,
} from "@/lib/overlays";
import {
  carouselCardWidthClasses,
  carouselCardWidthPx,
  type CardPresentation,
} from "@/lib/uiCustomization";
import { cn } from "@/lib/utils";

/** Made-up titles: the preview must never look like someone's real library. */
const SAMPLES: Record<
  OverlayPreviewVariant,
  { titleKey: MessageKey; metaKey: MessageKey; art: string }
> = {
  movie: {
    titleKey: "web.settings.interface.preview.movieTitle",
    metaKey: "web.settings.interface.preview.movieMeta",
    art: "from-sky-700 via-indigo-800 to-slate-950",
  },
  show: {
    titleKey: "web.settings.interface.preview.showTitle",
    metaKey: "web.settings.interface.preview.showMeta",
    art: "from-amber-600 via-rose-800 to-slate-950",
  },
  requested: {
    titleKey: "web.settings.interface.preview.requestedTitle",
    metaKey: "web.settings.interface.preview.requestedMeta",
    art: "from-emerald-700 via-teal-900 to-slate-950",
  },
};

// The gap between sample posters: gap-2.
const PREVIEW_GAP_PX = 8;

// The lg breakpoint, from which home-row cards take their largest width.
const LG_VIEWPORT_PX = 1024;

const SHORTCUT_TILE =
  "border-border/20 bg-background/80 flex size-6 items-center justify-center rounded-md border shadow-sm";

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
  const t = useT();
  // Sample posters are as wide as the profile's poster cards in a home row
  // at this window size, so badges show at the size they really draw at;
  // they scale with the card. Every sample shows at every size, wrapping
  // when the row runs out.
  const width = carouselCardWidthClasses(presentation.poster_size);
  // Beside the controls the panel holds two posters a row. That layout only
  // applies on wide screens, past lg.
  const rowWidth =
    2 * carouselCardWidthPx(presentation.poster_size, LG_VIEWPORT_PX) + PREVIEW_GAP_PX;
  const hasShortcuts = quickActionMode !== "none";

  return (
    <figure
      className="surface-panel-subtle rounded-2xl border p-4"
      aria-label={t("web.settings.interface.preview.title")}
    >
      <figcaption className="mb-3 flex items-baseline justify-between gap-3">
        <span className="text-sm font-semibold">{t("web.settings.interface.preview.title")}</span>
        <span className="text-muted-foreground text-[13px]">
          {t("web.settings.interface.preview.caption")}
        </span>
      </figcaption>
      <div
        className="flex min-h-56 flex-wrap items-start justify-center gap-2 @5xl:w-[var(--preview-row)]"
        style={{ "--preview-row": `${rowWidth}px` } as CSSProperties}
      >
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
                <CardWatchStatus userState={OVERLAY_PREVIEW_WATCH_STATES[variant]} />
                {hasShortcuts ? (
                  <div
                    aria-hidden="true"
                    className={cn(
                      // eslint-disable-next-line silo/no-physical-direction -- on the poster artwork, which doesn't mirror; matches CardOverlays and CardWatchStatus
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
              <div className="mt-1.5 min-w-0">
                <p className="truncate text-xs font-medium">{t(sample.titleKey)}</p>
                <p className="text-muted-foreground truncate text-xs">{t(sample.metaKey)}</p>
              </div>
            </div>
          );
        })}
      </div>
      {hasShortcuts ? (
        <p className="text-muted-foreground mt-3 text-[13px] leading-relaxed">
          {t("web.settings.interface.preview.shortcutsHint")}
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
  const {
    editablePrefs,
    overlaysEnabled,
    quickActionPreference,
    quickActionsEnabled,
    isOverlaySupported,
  } = useOverlayPrefs();
  // The request sample only shows a badge this server lets the profile keep.
  const variants = isOverlaySupported("request_status")
    ? OVERLAY_PREVIEW_VARIANTS
    : OVERLAY_PREVIEW_VARIANTS.filter((variant) => variant !== "requested");

  return (
    <PosterCardPreviewView
      presentation={cardPresentation}
      overlayPrefs={overlaysEnabled ? editablePrefs : null}
      quickActionMode={quickActionsEnabled ? quickActionPreference : "none"}
      variants={variants}
      revealShortcuts={revealShortcuts}
    />
  );
}
