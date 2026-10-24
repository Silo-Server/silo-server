import { Check, Download } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * Request stays hidden until the pointer is over the season card
 * (group/season), or focus reaches it. A touch screen has no
 * hover, so there it always shows. Requested always shows.
 */
const REVEAL_ON_HOVER =
  "opacity-0 transition-opacity group-hover/season:opacity-100 group-focus-within/season:opacity-100 focus-visible:opacity-100 [@media(pointer:coarse)]:opacity-100";

interface RequestSeasonButtonProps {
  /** What the request covers, for the accessible name, e.g. "Season 2". */
  target: string;
  /** The series' active request already covers this season. */
  requested: boolean;
  onRequest: () => void;
  /** Shows without hover, for a season page's own header. */
  alwaysVisible?: boolean;
  className?: string;
}

/**
 * The Request button on a season the library lacks. Once a request
 * covers the season it reads Requested and does nothing. It is marked
 * aria-disabled rather than disabled, so the disabled style does not fight the
 * hover reveal.
 */
export default function RequestSeasonButton({
  target,
  requested,
  onRequest,
  alwaysVisible = false,
  className,
}: RequestSeasonButtonProps) {
  return (
    <Button
      type="button"
      size="sm"
      variant="secondary"
      className={cn(
        "gap-1 text-xs",
        requested ? "cursor-default" : !alwaysVisible && REVEAL_ON_HOVER,
        className,
      )}
      onClick={requested ? undefined : onRequest}
      aria-disabled={requested || undefined}
      aria-label={requested ? `${target} requested` : `Request ${target}`}
    >
      {requested ? <Check className="size-3.5" /> : <Download className="size-3.5" />}
      {requested ? "Requested" : "Request"}
    </Button>
  );
}
