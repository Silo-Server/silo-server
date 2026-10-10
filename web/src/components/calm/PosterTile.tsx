import { useState } from "react";
import { decodeThumbhash } from "@/lib/thumbhash";
import { cn } from "@/lib/utils";

function thumbhashUrl(thumbhash: string | undefined): string {
  try {
    return thumbhash ? decodeThumbhash(thumbhash) : "";
  } catch {
    // A malformed thumbhash leaves the plain tile.
    return "";
  }
}

/**
 * A title's poster over its blurred thumbhash. Without a poster, or when the
 * poster fails to load, the blur (or the plain tile) shows instead.
 */
export function PosterArt({
  posterUrl,
  thumbhash,
  className,
}: {
  posterUrl?: string;
  thumbhash?: string;
  className?: string;
}) {
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  const blur = thumbhashUrl(thumbhash);
  return (
    <div
      className={cn("bg-muted overflow-hidden bg-cover bg-center", className)}
      style={blur ? { backgroundImage: `url(${blur})` } : undefined}
    >
      {posterUrl && posterUrl !== failedUrl ? (
        <img
          src={posterUrl}
          alt=""
          loading="lazy"
          decoding="async"
          onError={() => setFailedUrl(posterUrl)}
          className="size-full object-cover"
        />
      ) : null}
    </div>
  );
}

/** One title in a preview strip: its poster with the title over the bottom. */
export function PosterTile({
  title,
  posterUrl,
  thumbhash,
  className,
}: {
  title: string;
  posterUrl?: string;
  thumbhash?: string;
  className?: string;
}) {
  return (
    <figure
      className={cn("relative m-0 aspect-[2/3] shrink-0 overflow-hidden rounded-[10px]", className)}
    >
      <PosterArt
        posterUrl={posterUrl}
        thumbhash={thumbhash}
        className="ring-border/60 absolute inset-0 ring-1 ring-inset"
      />
      <figcaption className="absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/80 to-transparent px-2 pt-6 pb-2 text-[11.5px] leading-tight font-semibold text-white">
        <span className="line-clamp-2">{title}</span>
      </figcaption>
    </figure>
  );
}
