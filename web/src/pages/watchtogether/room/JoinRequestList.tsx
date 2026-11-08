import { useState } from "react";
import { Check, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { WatchTogetherJoinRequest } from "@/lib/watchTogether";
import { MemberAvatar } from "./MemberAvatar";

/**
 * Pending requests to join a public room, shown to the host above the
 * member list. Each row is answered once; the socket keeps the list current.
 */
export function JoinRequestList({
  requests,
  onDecide,
}: {
  requests: WatchTogetherJoinRequest[];
  onDecide: (requestId: string, approve: boolean) => Promise<void>;
}) {
  const [busyId, setBusyId] = useState<string | null>(null);
  if (requests.length === 0) return null;
  const decide = (requestId: string, approve: boolean) => {
    setBusyId(requestId);
    void onDecide(requestId, approve).finally(() => setBusyId(null));
  };
  return (
    <section aria-labelledby="join-requests" className="border-border/60 border-b py-2">
      <h3
        id="join-requests"
        className="text-muted-foreground px-4 py-1.5 text-[11px] font-semibold tracking-[0.14em] uppercase"
      >
        Asking to join · {requests.length}
      </h3>
      <ul className="flex flex-col">
        {requests.map((request) => (
          <li key={request.id} className="flex items-center gap-3 px-4 py-2">
            <MemberAvatar name={request.display_name} />
            <span className="min-w-0 flex-1 truncate text-sm font-medium">
              {request.display_name}
            </span>
            <Button
              type="button"
              size="icon"
              variant="outline"
              className="size-8"
              disabled={busyId === request.id}
              onClick={() => decide(request.id, false)}
              aria-label={`Turn away ${request.display_name}`}
            >
              <X className="size-4" />
            </Button>
            <Button
              type="button"
              size="icon"
              className="size-8"
              disabled={busyId === request.id}
              onClick={() => decide(request.id, true)}
              aria-label={`Let ${request.display_name} in`}
            >
              <Check className="size-4" />
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}
