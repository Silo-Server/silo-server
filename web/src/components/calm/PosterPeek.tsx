import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { PosterArt } from "./PosterTile";
import { usePeekLimiter, type PeekRequest } from "./usePeekLimiter";

/** Fanned like a row on Home; the third poster drops on phones. */
const POSITIONS = ["left-0", "left-[21px]", "left-[42px] max-sm:hidden"];

function IconTile({ icon: Icon }: { icon: LucideIcon }) {
  return (
    <div
      aria-hidden
      className="bg-accent/80 text-muted-foreground ring-border grid h-[42px] w-12 place-items-center rounded-xl ring-1 ring-inset sm:h-[50px] sm:w-[74px]"
    >
      <Icon className="size-5" />
    </div>
  );
}

function Posters({ icon, request }: { icon: LucideIcon; request: PeekRequest }) {
  const { observe, items } = usePeekLimiter(request);
  return (
    <div ref={observe} aria-hidden className="relative h-[42px] w-12 sm:h-[50px] sm:w-[74px]">
      {items.length === 0 ? (
        <IconTile icon={icon} />
      ) : (
        items
          .slice(0, POSITIONS.length)
          .map((item, index) => (
            <PosterArt
              key={item.id}
              posterUrl={item.posterUrl}
              thumbhash={item.thumbhash}
              className={cn(
                "ring-surface absolute top-px h-10 w-[27px] rounded-[5px] shadow-[0_6px_12px_-6px_rgb(0_0_0/0.9)] ring-2 group-hover/row:ring-[color-mix(in_srgb,var(--accent)_60%,var(--surface))] group-data-[selected]/row:ring-[color-mix(in_srgb,var(--accent)_75%,var(--surface))] sm:top-0.5 sm:h-[46px] sm:w-8 sm:rounded-md",
                POSITIONS[index],
              )}
            />
          ))
      )}
    </div>
  );
}

/**
 * The art at the start of a `ListRow`: its first titles' posters once the row
 * has reached the screen, or `icon` while they load and when there are none
 * to show (no peek on this surface, an error, nothing in it).
 */
export function PosterPeek({ icon, request }: { icon: LucideIcon; request: PeekRequest | null }) {
  return request ? <Posters icon={icon} request={request} /> : <IconTile icon={icon} />;
}
