import { decodeThumbhash } from "@/lib/thumbhash";
import { cn } from "@/lib/utils";

/** A notification row's poster; the thumbhash shows while the image loads.
 * The default size suits an inbox row; className overrides it. */
export function NotificationPoster({
  url,
  thumbhash,
  className,
}: {
  url?: string;
  thumbhash?: string;
  className?: string;
}) {
  const thumbhashUrl = thumbhash ? decodeThumbhash(thumbhash) : "";
  return (
    <div
      className={cn("bg-muted relative h-16 w-11 shrink-0 overflow-hidden rounded-md", className)}
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
      {url && <img src={url} alt="" className="h-full w-full object-cover" loading="lazy" />}
    </div>
  );
}
