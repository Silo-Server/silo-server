import { decodeThumbhash } from "@/lib/thumbhash";

/** A notification row's poster; the thumbhash shows while the image loads. */
export function NotificationPoster({ url, thumbhash }: { url?: string; thumbhash?: string }) {
  const thumbhashUrl = thumbhash ? decodeThumbhash(thumbhash) : "";
  return (
    <div
      className="bg-muted relative h-16 w-11 shrink-0 overflow-hidden rounded-md"
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
