import { cn } from "@/lib/utils";
import { OVERLAY_PREVIEW_VARIANTS, type OverlayPreviewVariant } from "@/lib/overlays";

interface OverlayPreviewVariantToggleProps {
  value: OverlayPreviewVariant;
  onChange: (value: OverlayPreviewVariant) => void;
  /** The samples offered; defaults to all of them. */
  variants?: readonly OverlayPreviewVariant[];
  className?: string;
}

/**
 * Pill pair that picks which sample data <OverlayPreviewCard /> renders. Shared
 * by the user Card Overlays page and the admin defaults editor so both can
 * preview show-only overlays (network, show status) and the request status of
 * a watchlist title the library doesn't have yet while editing. The choice
 * is local view state on both surfaces and is deliberately never persisted.
 */
export function OverlayPreviewVariantToggle({
  value,
  onChange,
  variants = OVERLAY_PREVIEW_VARIANTS,
  className,
}: OverlayPreviewVariantToggleProps) {
  return (
    <div className={cn("flex gap-1.5", className)} role="group" aria-label="Preview sample">
      {variants.map((variant) => (
        <button
          key={variant}
          type="button"
          onClick={() => onChange(variant)}
          aria-pressed={value === variant}
          className={cn(
            "rounded-full border px-3 py-1 text-xs font-medium capitalize transition-colors",
            value === variant
              ? "border-primary bg-primary/10 text-primary"
              : "border-border/60 hover:border-border text-muted-foreground",
          )}
        >
          {variant}
        </button>
      ))}
    </div>
  );
}
