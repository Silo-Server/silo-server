import { V2TimeoutError } from "@/api/v2/request";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

interface RefreshFailedNoticeProps {
  /** What failed, e.g. "Couldn't refresh this library." */
  message: string;
  error: unknown;
  onRetry: () => void;
  retrying?: boolean;
  className?: string;
}

/**
 * An inline notice for a read that failed while earlier data stays on
 * screen, such as a failed background refetch. The page keeps what it has;
 * the notice says so and offers Try again.
 */
export default function RefreshFailedNotice({
  message,
  error,
  onRetry,
  retrying = false,
  className,
}: RefreshFailedNoticeProps) {
  return (
    <div
      role="alert"
      className={cn(
        "surface-panel flex flex-wrap items-center justify-between gap-3 rounded-[1.4rem] border-0 px-5 py-4",
        className,
      )}
    >
      <p className="text-muted-foreground text-sm">
        {message}
        {error instanceof V2TimeoutError ? " The server isn't responding." : ""}
      </p>
      <Button type="button" variant="outline" size="sm" onClick={onRetry} disabled={retrying}>
        Try again
      </Button>
    </div>
  );
}
