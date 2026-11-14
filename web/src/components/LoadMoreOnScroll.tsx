import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useIntersectionObserver } from "@/hooks/useIntersectionObserver";
import { useT } from "@/i18n";

/**
 * The foot of a list that loads as the viewer scrolls: reaching it reads the
 * next page, and a failed read offers a retry. Without IntersectionObserver a
 * Load more button does the scrolling's job.
 */
export function LoadMoreOnScroll({
  hasNextPage,
  isFetchingNextPage,
  isError,
  onLoadMore,
  errorMessage,
  showSpinner = false,
}: {
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  /** The last next-page read failed. */
  isError: boolean;
  onLoadMore: () => void;
  errorMessage: string;
  /** Show a spinner while the next page loads, for lists without placeholders. */
  showSpinner?: boolean;
}) {
  const t = useT();
  const sentinelRef = useIntersectionObserver({
    onIntersect: onLoadMore,
    enabled: hasNextPage && !isFetchingNextPage && !isError,
  });
  if (!hasNextPage) return null;
  if (isFetchingNextPage) {
    return showSpinner ? (
      <div
        className="flex justify-center py-4"
        role="status"
        aria-label={t("web.common.ui.loadMore.loading")}
      >
        <Loader2 className="text-muted-foreground h-5 w-5 animate-spin" />
      </div>
    ) : null;
  }
  if (isError) {
    return (
      <div className="flex flex-col items-center gap-3 py-6 text-center">
        <p className="text-muted-foreground text-sm">{errorMessage}</p>
        <Button variant="outline" size="sm" onClick={onLoadMore}>
          {t("common.tryAgain")}
        </Button>
      </div>
    );
  }
  return (
    <div ref={sentinelRef} className="flex justify-center py-4">
      {typeof IntersectionObserver === "undefined" ? (
        <Button variant="outline" size="sm" onClick={onLoadMore}>
          {t("web.common.ui.loadMore.button")}
        </Button>
      ) : null}
    </div>
  );
}
