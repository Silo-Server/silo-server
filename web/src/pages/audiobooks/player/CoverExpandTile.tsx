import { Maximize2 } from "lucide-react";

interface CoverExpandTileProps {
  contentId: string;
  posterUrl?: string;
  title?: string;
  onExpand?: () => void;
}

export function CoverExpandTile({ contentId, posterUrl, title, onExpand }: CoverExpandTileProps) {
  return (
    <button
      type="button"
      onClick={onExpand}
      aria-label="Open Now Listening"
      className="group/tile bg-muted relative h-[54px] w-[36px] shrink-0 overflow-hidden rounded-md transition-transform hover:scale-[1.04] focus-visible:scale-[1.04] focus-visible:outline-none"
      style={{ viewTransitionName: `audiobook-cover-${contentId}` }}
    >
      {posterUrl ? (
        <img src={posterUrl} alt={title ?? ""} className="h-full w-full object-cover" />
      ) : null}
      <span className="pointer-events-none absolute inset-0 flex items-center justify-center bg-black/40 opacity-0 transition-opacity group-hover/tile:opacity-100 group-focus-visible/tile:opacity-100">
        <Maximize2 className="h-3.5 w-3.5 text-white" />
      </span>
    </button>
  );
}
