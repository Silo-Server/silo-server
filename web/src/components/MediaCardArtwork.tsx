import { useState, type ReactNode } from "react";
import { useImageLoaded } from "@/hooks/useImageLoaded";
import { decodeThumbhash } from "@/lib/thumbhash";
import { cn } from "@/lib/utils";

// Caption typography shared by poster cards, so a library title and a title
// known only from TMDB read the same under their artwork.
export const MEDIA_CARD_CAPTION_CLASS = "px-1 pt-3";
export const MEDIA_CARD_TITLE_CLASS =
  "block truncate text-[14px] font-semibold tracking-tight hover:underline";
export const MEDIA_CARD_META_CLASS =
  "text-muted-foreground mt-1 block truncate text-[11px] font-medium tracking-[0.14em] uppercase hover:underline";

// The centred hover action on a poster card: Play on library titles, Request
// on titles outside the library. media-card-play-trigger owns the hover/focus
// reveal so every card surface shares one rule (app.css), gated on the
// observed pointer (lib/pointerCapability.ts).
export const MEDIA_CARD_CENTER_ACTION_CLASS =
  "media-card-play-trigger bg-primary text-primary-foreground absolute top-1/2 left-1/2 z-10 flex -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full shadow-lg transition-all duration-200 hover:scale-110 hover:shadow-xl hover:brightness-110 active:scale-95";

// Library grid cards fade into the theme background; row cards and TMDB
// titles use a black scrim.
const SCRIM_CLASS = {
  background:
    "from-background/70 pointer-events-none absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t to-transparent opacity-90",
  black:
    "pointer-events-none absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t from-black/55 to-transparent opacity-90",
} as const;

interface MediaCardArtworkProps {
  src?: string | null;
  alt: string;
  /** Shown in place of a missing poster. */
  fallbackLabel: string;
  thumbhash?: string | null;
  square?: boolean;
  lazy?: boolean;
  scrim?: keyof typeof SCRIM_CLASS;
  /** Recedes the artwork, for titles the viewer can't act on. */
  dim?: boolean;
  /**
   * Shows the missing-poster fallback when the poster fails to load. Library
   * cards leave their thumbhash in place instead; TMDB titles have none, so
   * without this a failed poster is an empty box.
   */
  fallbackOnError?: boolean;
  /** Overlays drawn inside the rounded artwork box. */
  children?: ReactNode;
}

/**
 * The artwork box of a poster card: rounded frame, thumbhash placeholder,
 * fade-in on load, the missing-poster fallback, and the bottom scrim.
 */
export default function MediaCardArtwork({
  src,
  alt,
  fallbackLabel,
  thumbhash,
  square = false,
  lazy = false,
  scrim = "black",
  dim = false,
  fallbackOnError = false,
  children,
}: MediaCardArtworkProps) {
  const { loaded, onLoad, onError } = useImageLoaded(src);
  const thumbhashUrl = thumbhash ? decodeThumbhash(thumbhash) : "";
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const showImage = Boolean(src) && !(fallbackOnError && failedSrc === src);
  const dimClass = dim ? "brightness-[0.85] saturate-[0.8]" : "";

  return (
    <div
      className={`media-card-image relative ${square ? "aspect-square" : "aspect-[2/3]"}`}
      style={
        thumbhashUrl
          ? {
              backgroundImage: `url(${thumbhashUrl})`,
              backgroundSize: "cover",
              backgroundPosition: "center",
            }
          : undefined
      }
    >
      {showImage ? (
        <img
          src={src!}
          alt={alt}
          className={cn(
            "h-full w-full object-cover transition-opacity duration-300",
            loaded ? "opacity-100" : "opacity-0",
            dimClass,
          )}
          loading={lazy ? "lazy" : undefined}
          onLoad={onLoad}
          onError={() => {
            onError();
            setFailedSrc(src ?? null);
          }}
        />
      ) : (
        <div
          className={cn(
            "text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-sm",
            dimClass,
          )}
        >
          <span className="line-clamp-3 font-medium">{fallbackLabel || "No Poster"}</span>
        </div>
      )}
      <div className={SCRIM_CLASS[scrim]} />
      {children}
    </div>
  );
}
