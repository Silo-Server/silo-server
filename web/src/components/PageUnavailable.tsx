import type { ReactNode } from "react";
import { ChevronLeft, CircleAlert, SearchX } from "lucide-react";

import { Button } from "@/components/ui/button";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { hasEarlierEntry } from "@/lib/navigationHistory";

interface PageUnavailableProps {
  title: string;
  description: string;
  /**
   * Set when the read failed rather than came back missing: the page leads
   * with Try again, and the icon reads as an error instead of a miss.
   */
  onRetry?: () => void;
  retrying?: boolean;
  /** Page-specific ways forward, placed between Back and Go home. */
  children?: ReactNode;
}

/**
 * Stands in for a page whose subject cannot be shown, so the viewer always has
 * a way on: Back when there is an in-app entry behind them, and Go home.
 */
export default function PageUnavailable({
  title,
  description,
  onRetry,
  retrying = false,
  children,
}: PageUnavailableProps) {
  const navigate = useViewTransitionNavigate();
  // A cold entry — a shared link, a reload — has nothing behind it to go back to.
  const canGoBack = hasEarlierEntry();
  const Icon = onRetry ? CircleAlert : SearchX;

  return (
    <div className="page-shell flex min-h-[60dvh] flex-col items-center justify-center gap-6 py-16 text-center">
      <Icon aria-hidden="true" className="text-muted-foreground/50 size-10" />
      <div className="space-y-2">
        <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">{title}</h1>
        <p className="text-muted-foreground mx-auto max-w-md text-sm">{description}</p>
      </div>
      <div className="flex flex-wrap items-center justify-center gap-3">
        {onRetry && (
          <Button type="button" onClick={onRetry} disabled={retrying}>
            Try again
          </Button>
        )}
        {canGoBack && (
          <Button
            type="button"
            variant={onRetry ? "outline" : "default"}
            onClick={() => navigate(-1)}
          >
            <ChevronLeft />
            Back
          </Button>
        )}
        {children}
        <Button asChild variant={onRetry || canGoBack ? "outline" : "default"}>
          <ViewTransitionLink to="/">Go home</ViewTransitionLink>
        </Button>
      </div>
    </div>
  );
}
