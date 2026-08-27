import type { AdminSession } from "@/api/types";
import { describeSessionDelivery } from "@/lib/sessionTelemetry";

export function SessionDeliveryBadges({
  session,
  viewBlind,
}: {
  session: AdminSession;
  viewBlind: boolean;
}) {
  const delivery = describeSessionDelivery(session, { viewBlind });
  if (!delivery) return null;

  return (
    <div className="text-muted-foreground mb-1.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px]">
      <span className="text-foreground font-semibold">{delivery.rate ?? "measuring…"}</span>
      <span>{delivery.bytes} delivered</span>
      {delivery.degraded ? (
        <span title="Some records were dropped; this total is a floor.">at least</span>
      ) : null}
      {delivery.noDelivery ? (
        <span
          title="A client reports this as playing, but no bytes were measured leaving the server."
          className="border-destructive/40 text-destructive inline-flex rounded border px-1 py-px font-semibold"
        >
          delivering nothing
        </span>
      ) : null}
      {delivery.unclaimed ? (
        <span
          title="Bytes are going out, but no playback session manager claims this session."
          className="border-destructive/40 text-destructive inline-flex rounded border px-1 py-px font-semibold"
        >
          unclaimed
        </span>
      ) : null}
      {delivery.viewerIpCount > 1 ? (
        <span
          title="More than one address pulled bytes for this session."
          className="border-border/60 bg-muted/30 inline-flex rounded border px-1 py-px font-semibold"
        >
          {delivery.viewerIpCount} IPs
        </span>
      ) : null}
      {delivery.identityConflict ? (
        <span
          title="Publishers disagreed about who is watching this session."
          className="border-destructive/40 text-destructive inline-flex rounded border px-1 py-px font-semibold"
        >
          identity conflict
        </span>
      ) : null}
    </div>
  );
}
