import { useRef } from "react";
import { Loader2, Trash2, Upload } from "lucide-react";

import { cn } from "@/lib/utils";
import {
  useDeleteBrandingAsset,
  useUploadBrandingAsset,
  type BrandingAssetKind,
} from "@/hooks/queries/admin/branding";
import { BRANDING_ASSET_SPECS } from "./brandingAssetSpecs";

interface BrandingAssetFieldProps {
  label: string;
  description?: string;
  kind: BrandingAssetKind;
  /** Current asset URL (from the branding read), or null when using the default. */
  currentUrl: string | null;
  /** Accepted file types for the file input. */
  accept: string;
  /** When false, uploads are blocked (e.g. S3 not configured). */
  enabled: boolean;
  /** Square preview suits the icon/favicon; wide suits the wordmark/background. */
  preview?: "square" | "wide";
  /**
   * Forces the preview tile's backdrop. Light-theme assets are dark-on-transparent,
   * so they would be invisible on the default muted tile in a dark admin theme.
   */
  previewBg?: "light";
  /**
   * What an empty slot actually serves when the kind has no bundled default of
   * its own — the light variants fall back to the main logo/icon, so the
   * preview must show that image, not a placeholder.
   */
  fallbackUrl?: string | null;
}

export function BrandingAssetField({
  label,
  description,
  kind,
  currentUrl,
  accept,
  enabled,
  preview = "wide",
  previewBg,
  fallbackUrl,
}: BrandingAssetFieldProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const upload = useUploadBrandingAsset();
  const remove = useDeleteBrandingAsset();
  const busy = upload.isPending || remove.isPending;
  const spec = BRANDING_ASSET_SPECS[kind];
  // An empty slot is not "no image" — the bundled default (or, for the light
  // variants, the main asset they fall back to) is what visitors see. Show
  // that instead of a placeholder glyph, dimmed and captioned so it never
  // reads as the admin's own upload.
  const shownUrl = currentUrl ?? spec.defaultUrl ?? fallbackUrl ?? null;
  const showingDefault = currentUrl === null;

  const handleFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = ""; // allow re-selecting the same file
    if (file) {
      upload.mutate({ kind, file });
    }
  };

  return (
    <div className="border-border bg-background flex items-center gap-4 rounded-xl border p-3">
      <div className="flex shrink-0 flex-col items-center gap-1">
        <div
          className={cn(
            "border-border flex items-center justify-center overflow-hidden rounded-lg border",
            previewBg === "light" ? "bg-white" : "bg-muted/40",
            preview === "square" ? "h-14 w-14" : "h-14 w-28",
          )}
        >
          {shownUrl ? (
            <img
              src={shownUrl}
              alt={`${label} preview`}
              className={cn("h-full w-full object-contain", showingDefault && "opacity-40")}
            />
          ) : (
            <span
              aria-hidden
              className="from-primary/25 via-muted/40 h-full w-full bg-gradient-to-br to-transparent"
            />
          )}
        </div>
        {showingDefault && (
          <span className="text-muted-foreground text-[10px] leading-none">
            {spec.emptyCaption}
          </span>
        )}
      </div>

      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{label}</p>
        {description && <p className="text-muted-foreground mt-0.5 text-xs">{description}</p>}
        <p className="text-muted-foreground/80 mt-0.5 text-[11px] leading-relaxed">
          {spec.guidance}
        </p>
      </div>

      <input
        ref={inputRef}
        type="file"
        accept={accept}
        className="hidden"
        onChange={handleFile}
        disabled={!enabled || busy}
      />
      <div className="flex shrink-0 items-center gap-2">
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          disabled={!enabled || busy}
          className="border-border hover:bg-muted/50 inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50"
        >
          {upload.isPending ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Upload className="h-3.5 w-3.5" />
          )}
          {currentUrl ? "Replace" : "Upload"}
        </button>
        {currentUrl && (
          <button
            type="button"
            onClick={() => remove.mutate({ kind })}
            disabled={busy}
            aria-label={`Remove ${label}`}
            className="text-muted-foreground hover:text-destructive inline-flex items-center rounded-lg border border-transparent p-1.5 transition-colors disabled:opacity-50"
          >
            {remove.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Trash2 className="h-3.5 w-3.5" />
            )}
          </button>
        )}
      </div>
    </div>
  );
}
