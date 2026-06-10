import CardOverlays from "./CardOverlays";
import { SAMPLE_MOVIE_DATA, SAMPLE_SHOW_DATA, type CardOverlayPrefs } from "@/lib/overlays";

interface OverlayPreviewCardProps {
  prefs: CardOverlayPrefs;
  variant?: "movie" | "show";
  size?: "sm" | "md";
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
}: OverlayPreviewCardProps) {
  const data = variant === "show" ? SAMPLE_SHOW_DATA : SAMPLE_MOVIE_DATA;
  const sizeClass = SIZE_CLASSES[size];
  return (
    <div
      className={`bg-muted/40 relative mx-auto aspect-[2/3] ${sizeClass} overflow-hidden rounded-xl border`}
    >
      <div className="text-muted-foreground/30 flex h-full items-center justify-center text-xs font-medium tracking-wider uppercase">
        {variant === "show" ? "Show preview" : "Movie preview"}
      </div>
      <CardOverlays data={data} prefs={prefs} />
    </div>
  );
}
