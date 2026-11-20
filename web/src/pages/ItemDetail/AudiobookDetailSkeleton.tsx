import { cn } from "@/lib/utils";
import "@/pages/audiobooks/audiobooks.css";

const CHAPTER_ROWS = 6;

/**
 * Holds the book detail layout while an audiobook or ebook loads: the same
 * grid, cover aspect, actions, header lines, facts row, overview, and (for
 * audiobooks) chapter list, so nothing moves when the book arrives. `solid`
 * draws the fixed boxes a cold Home entry uses; otherwise the boxes pulse like
 * the other detail skeletons.
 */
export default function AudiobookDetailSkeleton({
  solid = false,
  kind = "audiobook",
}: {
  solid?: boolean;
  kind?: "audiobook" | "ebook";
}) {
  const isEbook = kind === "ebook";
  const block = (className: string) =>
    cn(solid ? "home-item-transition-block" : "bg-accent animate-pulse", className);

  return (
    <div
      aria-hidden="true"
      className={cn(
        "page-shell-wide audiobook-detail-grid pt-16 pb-14 sm:pt-20 lg:pt-24",
        isEbook && "audiobook-detail-grid--ebook",
      )}
    >
      <div className="audiobook-detail-side">
        <div
          className={block(
            cn(
              "audiobook-detail-cover rounded-xl lg:rounded-2xl",
              isEbook ? "aspect-[2/3]" : "aspect-square",
            ),
          )}
        />
        <div className="audiobook-detail-actions grid grid-cols-[minmax(0,1fr)] gap-3">
          <div className={block("h-10 w-full rounded-full")} />
          <div className="flex items-center gap-2">
            {isEbook ? (
              <>
                <div className={block("h-10 min-w-0 flex-1 rounded-full md:h-9")} />
                <div className={block("h-10 w-24 shrink-0 rounded-full md:h-9")} />
              </>
            ) : (
              <>
                <div className={block("size-9 shrink-0 rounded-full")} />
                <div className={block("size-9 shrink-0 rounded-full")} />
              </>
            )}
          </div>
        </div>
      </div>

      <div
        className={cn(
          "audiobook-detail-head grid grid-cols-[minmax(0,1fr)] gap-1.5 lg:gap-2 lg:pt-3",
          isEbook && "md:pt-1",
        )}
      >
        <div className={block("h-5 w-44 rounded-md")} />
        <div className={block("h-7 w-full max-w-lg rounded-lg sm:h-10 lg:mb-1 lg:h-[3.25rem]")} />
        <div className={block("h-5 w-48 rounded-md sm:h-6")} />
        {!isEbook && <div className={block("h-5 w-44 rounded-md sm:h-6")} />}
      </div>

      <div className="audiobook-detail-body grid grid-cols-[minmax(0,1fr)] gap-10 lg:gap-12">
        <div className="grid grid-cols-[minmax(0,1fr)] gap-6">
          <div className="border-border/60 grid grid-cols-2 gap-x-6 gap-y-3 border-y py-4 sm:flex sm:flex-wrap sm:gap-x-9">
            {["w-16", "w-14", "w-12", "w-24"].map((width, index) => (
              <div key={index} className="grid gap-1.5">
                <div className={block("h-3.5 w-12 rounded-md")} />
                <div className={block(`h-5 rounded-md ${width}`)} />
              </div>
            ))}
          </div>
          <div className="grid max-w-[68ch] gap-2">
            <div className={block("h-4 w-full rounded-md")} />
            <div className={block("h-4 w-full rounded-md")} />
            <div className={block("h-4 w-4/5 rounded-md")} />
          </div>
          <div className="flex flex-wrap gap-2">
            <div className={block("h-6 w-16 rounded-full")} />
            <div className={block("h-6 w-20 rounded-full")} />
            <div className={block("h-6 w-14 rounded-full")} />
          </div>
        </div>

        {!isEbook && (
          <div>
            <div className={block("mb-3 h-7 w-28 rounded-md")} />
            <div className="grid gap-0.5">
              {Array.from({ length: CHAPTER_ROWS }, (_, index) => (
                <div
                  key={index}
                  className="grid grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-3 px-3 py-2.5 sm:gap-4"
                >
                  <div className={block("ms-auto h-3 w-4 rounded-md")} />
                  <div className={block("h-4 w-3/5 rounded-md")} />
                  <div className={block("h-3 w-10 rounded-md")} />
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
