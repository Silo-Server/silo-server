import { useId } from "react";

import { PosterTile } from "@/components/calm/PosterTile";
import { Skeleton } from "@/components/ui/skeleton";
import type { ScopePreview } from "@/hooks/queries/collectionScope";
import {
  PREVIEW_EMPTY,
  PREVIEW_EMPTY_HELP,
  PREVIEW_FAILED,
  PREVIEW_LIVE,
  previewMatches,
} from "@/lib/collections/copy";
import { cn } from "@/lib/utils";

const GRID = "grid grid-cols-4 gap-2.5 sm:grid-cols-6 lg:grid-cols-8";
const GHOSTS = 8;

/**
 * "● Live preview · 40 titles match · showing 24": the first titles the
 * Smart rules match across every chosen library, as posters. With nothing to
 * match it shows ghost posters and says saving still works.
 */
export function CollectionPreviewPane({
  preview,
  note,
}: {
  preview: ScopePreview;
  /** A muted line beside the count, such as where the matches come from. */
  note?: string;
}) {
  const id = useId();
  const ready = preview.status === "ready" ? preview : null;
  const busy = preview.status === "loading" || Boolean(ready?.refreshing);

  return (
    <section
      aria-labelledby={`${id}-heading`}
      aria-busy={busy}
      className="surface-panel grid gap-3.5 rounded-[22px] p-5 sm:p-6"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[14px]">
        <h2
          id={`${id}-heading`}
          className="text-muted-foreground inline-flex items-center gap-2 font-normal"
        >
          <span
            aria-hidden
            className="bg-success ring-success/20 size-1.5 rounded-full ring-[3px]"
          />
          {PREVIEW_LIVE}
        </h2>
        {/* Read out once the debounced request answers, not on every keystroke. */}
        <p aria-live="polite" className="inline-flex flex-wrap items-center gap-x-2">
          {ready ? (
            <>
              <span aria-hidden className="text-muted-foreground">
                ·
              </span>
              <b className="font-semibold">{previewMatches(ready.total)}</b>
              {ready.items.length > 0 && ready.total > ready.items.length ? (
                <>
                  <span aria-hidden className="text-muted-foreground">
                    ·
                  </span>
                  <span className="text-muted-foreground">{`showing ${ready.items.length}`}</span>
                </>
              ) : null}
            </>
          ) : null}
        </p>
        {note ? (
          <>
            <span aria-hidden className="text-muted-foreground">
              ·
            </span>
            <span className="text-muted-foreground">{note}</span>
          </>
        ) : null}
      </div>

      <PreviewBody preview={preview} />
    </section>
  );
}

/** Posters, loading skeletons, or ghost posters with the reason there are none. */
function PreviewBody({ preview }: { preview: ScopePreview }) {
  if (preview.status === "loading") {
    return (
      <div className={GRID}>
        {Array.from({ length: GHOSTS }, (_, index) => (
          <Skeleton key={index} className="aspect-[2/3] rounded-[10px]" />
        ))}
      </div>
    );
  }
  if (preview.status === "ready" && preview.items.length > 0) {
    return (
      <ul
        aria-label="Matching titles"
        className={cn(GRID, "transition-opacity", preview.refreshing && "opacity-60")}
      >
        {preview.items.map((item) => (
          <li key={item.content_id}>
            <PosterTile title={item.title} posterUrl={item.poster_url} className="w-full" />
          </li>
        ))}
      </ul>
    );
  }
  let message = <p className="text-muted-foreground text-[13px]">{PREVIEW_FAILED}</p>;
  if (preview.status === "ready") {
    message = (
      <>
        <p className="text-[14.5px] font-semibold">{PREVIEW_EMPTY}</p>
        <p className="text-muted-foreground mt-1 text-[13px]">{PREVIEW_EMPTY_HELP}</p>
      </>
    );
  }
  // The count above already reads out "0 titles match"; announce only the other reasons.
  return (
    <div className="grid gap-4">
      <div aria-hidden className={GRID}>
        {Array.from({ length: GHOSTS }, (_, index) => (
          <span
            key={index}
            className="border-border/70 bg-muted/20 aspect-[2/3] rounded-[10px] border"
          />
        ))}
      </div>
      <div className="text-center" role={preview.status === "ready" ? undefined : "status"}>
        {message}
      </div>
    </div>
  );
}
