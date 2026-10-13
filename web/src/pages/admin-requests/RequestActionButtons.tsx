import { Ban, Check, Loader2, RefreshCw, X } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { MediaRequest } from "@/api/types";
import { Button } from "@/components/ui/button";
import { requestQueueActions, type RequestQueueAction } from "./requestQueueModel";

/** Runs a queue action; decline and cancel ask for an optional reason first. */
export interface RequestQueueActionHandlers {
  run: (action: RequestQueueAction, request: MediaRequest) => void;
  /** The action for this request that is on its way to the server, if any. */
  busyAction: (id: string) => RequestQueueAction | undefined;
  /** Every action waits, as while a bulk action runs. */
  locked: boolean;
}

const ACTIONS: Record<
  RequestQueueAction,
  { label: string; icon: LucideIcon; variant: "default" | "outline" }
> = {
  approve: { label: "Approve", icon: Check, variant: "default" },
  decline: { label: "Decline", icon: X, variant: "outline" },
  retry: { label: "Retry", icon: RefreshCw, variant: "default" },
  cancel: { label: "Cancel request", icon: Ban, variant: "outline" },
};

/** The actions a request allows in the queue; none for one that is done. */
export function RequestActionButtons({
  request,
  handlers,
}: {
  request: MediaRequest;
  handlers: RequestQueueActionHandlers;
}) {
  const actions = requestQueueActions(request);
  if (actions.length === 0) return null;
  const running = handlers.busyAction(request.id);
  const disabled = handlers.locked || running !== undefined;
  return (
    <div className="flex flex-wrap gap-2">
      {actions.map((action) => {
        const { label, icon: Icon, variant } = ACTIONS[action];
        const isRunning = action === running;
        return (
          <Button
            key={action}
            size="sm"
            variant={variant}
            disabled={disabled}
            aria-label={`${label}: ${request.title}`}
            aria-busy={isRunning || undefined}
            onClick={() => handlers.run(action, request)}
          >
            {isRunning ? (
              <Loader2 aria-hidden="true" className="animate-spin" />
            ) : (
              <Icon aria-hidden="true" />
            )}
            {label}
          </Button>
        );
      })}
    </div>
  );
}
