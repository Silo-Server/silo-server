import { useState, type ReactNode } from "react";
import { decodeThumbhash } from "@/lib/thumbhash";
import type { PlayerBackdrop } from "../types";
import "@/styles/player-hud.css";

/**
 * The black screen a title loads behind, with its backdrop dimmed under a
 * spinner. The plan read and the first frame both show it, so the backdrop
 * stays put from one to the other. The thumbhash paints at once; the image
 * fades in over it once loaded.
 */
export function PlayerLoadingCover({
  backdrop,
  label,
  className = "",
  children,
}: {
  backdrop?: PlayerBackdrop;
  /** Read to screen readers as the cover's status. */
  label: string;
  className?: string;
  /** Shown under the spinner. */
  children?: ReactNode;
}) {
  const [loadedUrl, setLoadedUrl] = useState<string | null>(null);
  const placeholder = backdrop?.thumbhash ? decodeThumbhash(backdrop.thumbhash) : "";
  return (
    <div
      role="status"
      aria-label={label}
      className={`hud flex items-center justify-center overflow-hidden bg-black ${className}`}
    >
      {backdrop?.url || placeholder ? (
        <div
          aria-hidden="true"
          className="hud-loading-backdrop absolute inset-0 bg-cover bg-center"
          style={placeholder ? { backgroundImage: `url(${placeholder})` } : undefined}
        >
          {backdrop?.url ? (
            <img
              src={backdrop.url}
              alt=""
              decoding="async"
              // A cached image can finish before React attaches onLoad.
              ref={(img) => {
                if (img?.complete && img.naturalWidth > 0) setLoadedUrl(backdrop.url!);
              }}
              onLoad={() => setLoadedUrl(backdrop.url!)}
              className={`size-full object-cover transition-opacity duration-500 ${
                loadedUrl === backdrop.url ? "opacity-100" : "opacity-0"
              }`}
            />
          ) : null}
        </div>
      ) : null}
      <div className="relative flex flex-col items-center gap-4">
        <div className="hud-spinner" />
        {children}
      </div>
    </div>
  );
}
