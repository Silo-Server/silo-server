import { useT, type MessageKey } from "@/i18n";
import { cn } from "@/lib/utils";
import { OVERLAY_PREVIEW_VARIANTS, type OverlayPreviewVariant } from "@/lib/overlays";

const VARIANT_LABELS: Record<OverlayPreviewVariant, MessageKey> = {
  movie: "web.common.overlays.previewVariant.movie",
  show: "web.common.overlays.previewVariant.show",
  requested: "web.common.overlays.previewVariant.requested",
};

interface OverlayPreviewVariantToggleProps {
  value: OverlayPreviewVariant;
  onChange: (value: OverlayPreviewVariant) => void;
  /** The samples offered; defaults to all of them. */
  variants?: readonly OverlayPreviewVariant[];
  className?: string;
}

/**
 * Pill pair that picks which sample data <OverlayPreviewCard /> renders in the
 * admin defaults editor, so it can preview show-only overlays (network, show
 * status) and the request status of a watchlist title the library doesn't
 * have yet while editing. The choice is local view state and is deliberately
 * never persisted.
 */
export function OverlayPreviewVariantToggle({
  value,
  onChange,
  variants = OVERLAY_PREVIEW_VARIANTS,
  className,
}: OverlayPreviewVariantToggleProps) {
  const t = useT();
  return (
    <div
      className={cn("flex gap-1.5", className)}
      role="group"
      aria-label={t("web.common.overlays.previewSample")}
    >
      {variants.map((variant) => (
        <button
          key={variant}
          type="button"
          onClick={() => onChange(variant)}
          aria-pressed={value === variant}
          className={cn(
            "rounded-full border px-3 py-1 text-xs font-medium transition-colors",
            value === variant
              ? "border-primary bg-primary/10 text-primary"
              : "border-border/60 hover:border-border text-muted-foreground",
          )}
        >
          {t(VARIANT_LABELS[variant])}
        </button>
      ))}
    </div>
  );
}
