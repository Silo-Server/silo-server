import CardOverlays from "./CardOverlays";
import CardWatchStatus from "./CardWatchStatus";
import { RequestDownloadBar } from "./RequestDownloadBar";
import {
  OVERLAY_PREVIEW_SAMPLES,
  OVERLAY_PREVIEW_WATCH_STATES,
  requestDownloadBarPercent,
  type CardOverlayPrefs,
  type OverlayPreviewVariant,
} from "@/lib/overlays";
import { carouselCardWidthClasses, type PosterSize } from "@/lib/uiCustomization";

/**
 * Which sample item the preview stands in for. Picked by
 * <OverlayPreviewVariantToggle />. "requested" is a watchlist title the
 * library doesn't have yet, downloading.
 */
export type { OverlayPreviewVariant };

const PLACEHOLDER_LABEL: Record<OverlayPreviewVariant, string> = {
  movie: "Movie preview",
  show: "Show preview",
  requested: "Requested preview",
};

interface OverlayPreviewCardProps {
  prefs: CardOverlayPrefs;
  variant?: OverlayPreviewVariant;
  /** The poster size to draw at; the card is as wide as a home-row card. */
  posterSize?: PosterSize;
  showPosterOverlays?: boolean;
}

// Preview used by the admin defaults editor. Renders a 2:3 poster
// placeholder with the actual <CardOverlays /> renderer on top of it,
// fed sample data, at the width a home-row card draws at so badges show at
// their real size.
export function OverlayPreviewCard({
  prefs,
  variant = "movie",
  posterSize = "standard",
  showPosterOverlays = true,
}: OverlayPreviewCardProps) {
  const data = OVERLAY_PREVIEW_SAMPLES[variant];
  const downloadPercent = showPosterOverlays ? requestDownloadBarPercent(data, prefs) : null;
  return (
    <div className={`mx-auto ${carouselCardWidthClasses(posterSize)}`}>
      <div className="bg-muted/40 relative aspect-[2/3] overflow-hidden rounded-xl border">
        <div className="text-muted-foreground/30 flex h-full items-center justify-center text-xs font-medium tracking-wider uppercase">
          {PLACEHOLDER_LABEL[variant]}
        </div>
        {downloadPercent !== null ? <RequestDownloadBar percent={downloadPercent} /> : null}
        {showPosterOverlays ? (
          <CardOverlays data={data} prefs={prefs} hasProgressBar={downloadPercent !== null} />
        ) : null}
        <CardWatchStatus userState={OVERLAY_PREVIEW_WATCH_STATES[variant]} />
      </div>
    </div>
  );
}
