import CardOverlays from "./CardOverlays";
import { RequestDownloadBar } from "./RequestDownloadBar";
import {
  OVERLAY_PREVIEW_SAMPLES,
  requestDownloadBarPercent,
  type CardOverlayPrefs,
  type OverlayPreviewVariant,
} from "@/lib/overlays";

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
  size?: "sm" | "md";
  showPosterOverlays?: boolean;
}

const SIZE_CLASSES: Record<NonNullable<OverlayPreviewCardProps["size"]>, string> = {
  sm: "w-[140px]",
  md: "w-[180px]",
};

// Shared preview component used by both the user-facing card overlays
// settings page and the admin defaults editor. Renders a 2:3 poster
// placeholder with the actual <CardOverlays /> renderer on top of it,
// fed sample data.
export function OverlayPreviewCard({
  prefs,
  variant = "movie",
  size = "md",
  showPosterOverlays = true,
}: OverlayPreviewCardProps) {
  const data = OVERLAY_PREVIEW_SAMPLES[variant];
  const sizeClass = SIZE_CLASSES[size];
  const downloadPercent = showPosterOverlays ? requestDownloadBarPercent(data, prefs) : null;
  return (
    <div className={`mx-auto ${sizeClass}`}>
      <div className="bg-muted/40 relative aspect-[2/3] overflow-hidden rounded-xl border">
        <div className="text-muted-foreground/30 flex h-full items-center justify-center text-xs font-medium tracking-wider uppercase">
          {PLACEHOLDER_LABEL[variant]}
        </div>
        {downloadPercent !== null ? <RequestDownloadBar percent={downloadPercent} /> : null}
        {showPosterOverlays ? (
          <CardOverlays data={data} prefs={prefs} hasProgressBar={downloadPercent !== null} />
        ) : null}
      </div>
    </div>
  );
}
