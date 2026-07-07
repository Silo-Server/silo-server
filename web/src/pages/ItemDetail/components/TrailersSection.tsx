import { useState } from "react";
import { ChevronLeft, ChevronRight, Play } from "lucide-react";
import type { ItemVideo } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { extraKindLabel } from "@/lib/extraKinds";
import TrailerModal from "./TrailerModal";

interface TrailersSectionProps {
  videos: ItemVideo[];
}

/**
 * Horizontal carousel of remote provider videos. Only YouTube-hosted videos
 * are rendered (no embed support for other sites yet); thumbnails come
 * straight from YouTube, so no server call is needed. Server order is kept
 * (trailers first, official first).
 */
export default function TrailersSection({ videos }: TrailersSectionProps) {
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla();
  const [activeVideo, setActiveVideo] = useState<ItemVideo | null>(null);

  const playable = videos.filter((video) => video.site.toLowerCase() === "youtube");
  if (playable.length === 0) return null;

  return (
    <div>
      <h2 className="mb-5 text-xl font-semibold tracking-tight">Trailers &amp; More</h2>
      <div className="group/carousel relative">
        {canScrollPrev && (
          <button
            type="button"
            onClick={scrollPrev}
            className="from-background/90 absolute top-0 bottom-0 left-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100"
            aria-label="Scroll left"
          >
            <ChevronLeft className="text-foreground h-6 w-6" />
          </button>
        )}

        <div ref={emblaRef} className="embla__viewport overflow-hidden">
          <ul role="list" className="embla__container flex cursor-grab list-none gap-3">
            {playable.map((video) => (
              <li key={`${video.site}-${video.site_key}`} className="embla__slide shrink-0">
                <TrailerCard video={video} onPlay={() => setActiveVideo(video)} />
              </li>
            ))}
          </ul>
        </div>

        {canScrollNext && (
          <button
            type="button"
            onClick={scrollNext}
            className="from-background/90 absolute top-0 right-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100"
            aria-label="Scroll right"
          >
            <ChevronRight className="text-foreground h-6 w-6" />
          </button>
        )}
      </div>

      <TrailerModal video={activeVideo} onOpenChange={(open) => !open && setActiveVideo(null)} />
    </div>
  );
}

function TrailerCard({ video, onPlay }: { video: ItemVideo; onPlay: () => void }) {
  const label = video.name || extraKindLabel(video.kind);

  return (
    <button
      type="button"
      onClick={onPlay}
      className="group/trailer block w-[240px] text-left sm:w-[280px]"
    >
      <div className="media-card-image relative mb-2.5 aspect-video overflow-hidden rounded-lg">
        <img
          src={`https://i.ytimg.com/vi/${video.site_key}/hqdefault.jpg`}
          alt={label}
          className="h-full w-full object-cover transition-transform duration-300 group-hover/trailer:scale-105"
          loading="lazy"
        />
        <div className="absolute inset-0 flex items-center justify-center bg-black/0 transition-colors duration-200 group-hover/trailer:bg-black/30">
          <span className="flex h-11 w-11 items-center justify-center rounded-full bg-black/60 opacity-0 backdrop-blur-sm transition-opacity duration-200 group-hover/trailer:opacity-100 group-focus-visible/trailer:opacity-100">
            <Play className="ml-0.5 h-5 w-5 fill-white text-white" />
          </span>
        </div>
      </div>
      <div className="px-0.5">
        <div className="text-foreground truncate text-[13px] font-medium">{label}</div>
        <div className="text-muted-foreground mt-0.5 flex items-center gap-1.5 text-[11px]">
          <span>{extraKindLabel(video.kind)}</span>
          {video.is_official && (
            <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
              Official
            </Badge>
          )}
        </div>
      </div>
    </button>
  );
}
