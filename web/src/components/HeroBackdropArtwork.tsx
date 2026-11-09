import { imageIdentity, useImageLoaded } from "@/hooks/useImageLoaded";
import { decodeThumbhash } from "@/lib/thumbhash";
import { cn } from "@/lib/utils";

interface HeroBackdropArtworkProps {
  backdropUrl?: string;
  backdropThumbhash?: string;
  /** Positions the artwork; the caller draws its own scrim over it. */
  className?: string;
}

/**
 * A hero's backdrop: the thumbhash placeholder first, then the image fading in
 * over it. Renders nothing when there is neither.
 */
export function HeroBackdropArtwork({
  backdropUrl,
  backdropThumbhash,
  className,
}: HeroBackdropArtworkProps) {
  const { loaded, onLoad, onError } = useImageLoaded(backdropUrl);
  const placeholder = backdropThumbhash ? decodeThumbhash(backdropThumbhash) : "";
  if (!backdropUrl && !placeholder) return null;
  return (
    <div
      className={cn("hero-backdrop-artwork", className)}
      style={
        placeholder
          ? {
              backgroundImage: `url(${placeholder})`,
              backgroundSize: "cover",
              backgroundPosition: "center 20%",
            }
          : undefined
      }
    >
      {backdropUrl && (
        <img
          key={imageIdentity(backdropUrl)}
          src={backdropUrl}
          alt=""
          decoding="async"
          className={`h-full w-full object-cover object-[center_20%] transition-opacity duration-300 ${loaded ? "opacity-100" : "opacity-0"}`}
          onLoad={onLoad}
          onError={onError}
        />
      )}
    </div>
  );
}
