import { useId, useState } from "react";
import { ChevronDown, HelpCircle, LogOut, Monitor, Smartphone, Tv } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";
import { loginSessionDevice, sessionLastSeen } from "@/lib/loginSessionDevice";
import type { LoginSession } from "@/hooks/queries/loginSessions";

export function LoginSessionRow({
  session,
  onSignOut,
}: {
  session: LoginSession;
  onSignOut?: (session: LoginSession) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const detailsId = useId();
  const device = loginSessionDevice(session);
  const Icon =
    device.kind === "tv"
      ? Tv
      : device.kind === "phone"
        ? Smartphone
        : device.kind === "desktop"
          ? Monitor
          : HelpCircle;
  const lastSeen = sessionLastSeen(session.last_seen_at);
  return (
    <li className="surface-panel-subtle px-3 py-3 sm:px-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="border-border text-muted-foreground flex size-9 shrink-0 items-center justify-center rounded-xl border">
          <Icon className="size-4" aria-hidden="true" />
        </span>
        <div className="min-w-0 flex-1 basis-36">
          <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
            <span>{device.name}</span>
            {session.current && <Badge variant="outline">This browser</Badge>}
          </div>
          <p className="text-muted-foreground mt-1 text-xs">
            <span
              className={
                lastSeen === "Active now" ? "text-emerald-600 dark:text-emerald-400" : undefined
              }
            >
              {lastSeen}
            </span>{" "}
            · Signed in {formatDateTime(session.created_at, { seconds: false })}
          </p>
        </div>
        <div className="ml-auto flex items-center gap-1">
          {onSignOut && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              aria-label={session.current ? "Sign out this browser" : `Sign out ${device.name}`}
              onClick={() => onSignOut(session)}
            >
              {session.current && <LogOut aria-hidden="true" />}
              {session.current ? "Sign out this browser" : "Sign out"}
            </Button>
          )}
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="text-muted-foreground hover:text-foreground"
            aria-label={`Details for ${device.name}`}
            aria-expanded={expanded}
            aria-controls={detailsId}
            onClick={() => setExpanded(!expanded)}
          >
            Details
            <ChevronDown
              className={cn("transition-transform", expanded && "rotate-180")}
              aria-hidden="true"
            />
          </Button>
        </div>
      </div>
      {expanded && (
        <dl
          id={detailsId}
          className="border-border mt-3 grid grid-cols-1 gap-3 border-t pt-3 text-xs sm:grid-cols-2 sm:pl-12"
        >
          {[
            [
              "Last seen",
              session.last_seen_at ? formatDateTime(session.last_seen_at) : "Not recorded yet",
            ],
            ["Signed in", formatDateTime(session.created_at)],
            ["IP address at sign-in", session.ip_address || "Unknown"],
            ["Expires", formatDateTime(session.expires_at)],
          ].map(([label, value]) => (
            <div key={label}>
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="mt-1 break-words">{value}</dd>
            </div>
          ))}
          <div className="sm:col-span-2">
            <dt className="text-muted-foreground">User-Agent</dt>
            <dd className="mt-1 font-mono break-all">{session.device_name || "Not sent"}</dd>
          </div>
        </dl>
      )}
    </li>
  );
}
