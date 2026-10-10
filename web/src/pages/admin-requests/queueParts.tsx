import { useId, useState } from "react";
import type { ReactNode } from "react";
import { Film } from "lucide-react";
import type { MediaRequest, RequestTarget } from "@/api/types";
import { RequestStatusBadge } from "@/components/RequestStatusBadge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { requestDisplayState, tmdbImageURL } from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";
import { formatTargetStatus, requestProblemText } from "./requestQueueModel";

export function RowsSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }).map((_, index) => (
        <Skeleton key={index} className="h-16 rounded-lg" />
      ))}
    </div>
  );
}

export function EmptyPanel({
  title,
  detail,
  children,
}: {
  title: string;
  detail: string;
  children?: ReactNode;
}) {
  return (
    <div className="border-border bg-card flex flex-col items-center justify-center gap-2 rounded-lg border px-4 py-10 text-center">
      <p className="text-sm font-semibold">{title}</p>
      <p className="text-muted-foreground max-w-sm text-sm">{detail}</p>
      {children}
    </div>
  );
}

/** A request's TMDB poster, or a plain tile when it has none. */
export function RequestPoster({
  request,
  size = "w92",
  className,
}: {
  request: Pick<MediaRequest, "poster_path">;
  size?: string;
  className?: string;
}) {
  const src = tmdbImageURL(request.poster_path, size);
  return (
    <div
      className={cn(
        "bg-muted text-muted-foreground flex aspect-[2/3] shrink-0 items-center justify-center overflow-hidden rounded-md",
        className,
      )}
    >
      {src ? (
        <img src={src} alt="" loading="lazy" className="h-full w-full object-cover" />
      ) : (
        <Film className="h-4 w-4" aria-hidden="true" />
      )}
    </div>
  );
}

/** The state a user sees, with the reason it closed or the error that stopped it. */
export function RequestStateSummary({ request }: { request: MediaRequest }) {
  const state = requestDisplayState(request.status, request.outcome, request.state);
  const problem = requestProblemText(request);
  return (
    <div className="flex min-w-0 flex-col items-start gap-1">
      {state ? <RequestStatusBadge state={state} /> : null}
      {problem ? (
        <p
          className={cn(
            "line-clamp-3 text-xs break-words",
            problem.error ? "text-destructive" : "text-muted-foreground",
          )}
        >
          {problem.text}
        </p>
      ) : null}
    </div>
  );
}

export function TargetStatusBadge({ target }: { target: Pick<RequestTarget, "status"> }) {
  return (
    <Badge variant={target.status === "failed" ? "destructive" : "secondary"}>
      {formatTargetStatus(target.status)}
    </Badge>
  );
}

/**
 * Asks for the optional note a decline or cancellation carries. The note
 * starts empty each time the dialog opens.
 */
export function ReasonDialog({
  open,
  title,
  description,
  confirmLabel,
  pending = false,
  onConfirm,
  onOpenChange,
}: {
  open: boolean;
  title: string;
  description: string;
  confirmLabel: string;
  pending?: boolean;
  onConfirm: (reason: string) => void;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {open ? (
          <ReasonForm
            title={title}
            description={description}
            confirmLabel={confirmLabel}
            pending={pending}
            onConfirm={onConfirm}
            onCancel={() => onOpenChange(false)}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function ReasonForm({
  title,
  description,
  confirmLabel,
  pending,
  onConfirm,
  onCancel,
}: {
  title: string;
  description: string;
  confirmLabel: string;
  pending: boolean;
  onConfirm: (reason: string) => void;
  onCancel: () => void;
}) {
  const id = useId();
  const [reason, setReason] = useState("");
  return (
    <form
      className="grid gap-4"
      onSubmit={(event) => {
        event.preventDefault();
        onConfirm(reason.trim());
      }}
    >
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>
      <div className="grid gap-2">
        <Label htmlFor={id} className="text-sm">
          Reason (optional)
        </Label>
        <textarea
          id={id}
          className="border-input bg-background text-foreground focus-visible:ring-ring min-h-[88px] w-full rounded-md border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:outline-none"
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          placeholder="e.g. duplicate of an existing request"
        />
      </div>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Keep request
        </Button>
        <Button type="submit" disabled={pending}>
          {confirmLabel}
        </Button>
      </DialogFooter>
    </form>
  );
}
