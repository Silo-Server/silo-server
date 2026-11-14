import { useId, type ReactNode } from "react";

import { PosterTile } from "@/components/calm/PosterTile";
import { Skeleton } from "@/components/ui/skeleton";
import type { ScopePreview } from "@/hooks/queries/collectionScope";
import {
  previewEmpty,
  previewEmptyHelp,
  previewFailed,
  previewLive,
  previewNeedsRule,
  previewNeedsRuleHelp,
  previewMatches,
} from "@/lib/collections/copy";
import { cn } from "@/lib/utils";
import { useT } from "@/i18n";

const GRID = "grid grid-cols-4 gap-2.5 sm:grid-cols-6 lg:grid-cols-8";
const GHOSTS = 8;
/** A separator, the same in every language. */
const DOT = "·";

/**
 * "● Live preview · 40 titles match · showing 24": the first titles the
 * Smart rules match, from the libraries they name, as posters. With nothing to
 * match it shows ghost posters and says saving still works. With no complete
 * rule it previews nothing, since no rule would match every title, and asks
 * for one.
 */
export function CollectionPreviewPane({
  preview,
  needsRules = false,
}: {
  preview: ScopePreview;
  /** No rule is complete yet: show the prompt to add one, not the last result. */
  needsRules?: boolean;
}) {
  const t = useT();
  const id = useId();
  const ready = !needsRules && preview.status === "ready" ? preview : null;
  const busy = !needsRules && (preview.status === "loading" || Boolean(ready?.refreshing));

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
          {previewLive()}
        </h2>
        {/* Read out once the debounced request answers, not on every keystroke. */}
        <p aria-live="polite" className="inline-flex flex-wrap items-center gap-x-2">
          {ready ? (
            <>
              <span aria-hidden className="text-muted-foreground">
                {DOT}
              </span>
              <b className="font-semibold">{previewMatches(ready.total)}</b>
              {ready.items.length > 0 && ready.total > ready.items.length ? (
                <>
                  <span aria-hidden className="text-muted-foreground">
                    {DOT}
                  </span>
                  <span className="text-muted-foreground">
                    {t("web.collections.preview.showing", { shown: ready.items.length })}
                  </span>
                </>
              ) : null}
            </>
          ) : null}
        </p>
      </div>

      <PreviewBody preview={preview} needsRules={needsRules} />
    </section>
  );
}

/** Posters, loading skeletons, or ghost posters with the reason there are none. */
function PreviewBody({ preview, needsRules }: { preview: ScopePreview; needsRules: boolean }) {
  const t = useT();
  if (needsRules) {
    return (
      <PreviewGhosts announce>
        <p className="text-[14.5px] font-semibold">{previewNeedsRule()}</p>
        <p className="text-muted-foreground mt-1 text-[13px]">{previewNeedsRuleHelp()}</p>
      </PreviewGhosts>
    );
  }
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
        aria-label={t("web.collections.preview.matchingTitles")}
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
  let message = <p className="text-muted-foreground text-[13px]">{previewFailed()}</p>;
  if (preview.status === "ready") {
    message = (
      <>
        <p className="text-[14.5px] font-semibold">{previewEmpty()}</p>
        <p className="text-muted-foreground mt-1 text-[13px]">{previewEmptyHelp()}</p>
      </>
    );
  }
  // The count above already reads out "0 titles match"; announce only the other reasons.
  return <PreviewGhosts announce={preview.status !== "ready"}>{message}</PreviewGhosts>;
}

/** Ghost posters over the reason the preview shows no titles, read out when `announce`. */
function PreviewGhosts({ announce, children }: { announce: boolean; children: ReactNode }) {
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
      <div className="text-center" role={announce ? "status" : undefined}>
        {children}
      </div>
    </div>
  );
}
