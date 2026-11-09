import type { AdminDeviceDetail } from "@/api/types";
import { formatRelative } from "@/components/admin/deviceOverrides";
import { Button } from "@/components/ui/button";
import { formatConnectionRateKbps } from "@/lib/bitrateOptions";
import { formatDate } from "@/lib/datetime";
import { useResetAdminDeviceLearnedLimit } from "@/hooks/queries/admin/users";

type DeviceConnection = NonNullable<AdminDeviceDetail["connections"]>[number];

const TRANSPORT_LABELS: Record<string, string> = {
  wifi: "Wi-Fi",
  ethernet: "Ethernet",
  cellular: "Cellular",
  other: "Other network",
};

/** The last action on the latest plan, as `plan_summary` reports it. */
const BUFFERING_ACTION_LABELS: Record<string, string> = {
  none: "no buffering action",
  ignored: "buffering seen, lowering off",
  lowered: "lowered automatically",
  restored: "quality restored",
  accepted: "lowered when asked",
  kept: "quality kept when asked",
  timed_out: "prompt not answered",
  cant_lower: "could not lower",
};

function connectionLabel(connection: string) {
  if (connection === "local") return "Local network";
  if (connection === "remote") return "Remote connection";
  if (connection.startsWith("provider:")) return `Network access: ${connection.slice(9)}`;
  return connection;
}

function planSummaryText(summary: NonNullable<DeviceConnection["plan_summary"]>) {
  const stalls = Number(summary.diagnostics.stall_count ?? 0);
  const seconds = Math.round(Number(summary.diagnostics.stall_total_ms ?? 0) / 1000);
  const action = summary.diagnostics.buffering_action ?? "none";
  return [
    `${stalls} ${stalls === 1 ? "stall" : "stalls"}`,
    `${seconds} ${seconds === 1 ? "second" : "seconds"} buffering`,
    BUFFERING_ACTION_LABELS[action] ?? action,
    formatRelative(summary.reported_at),
  ].join(" · ");
}

/**
 * Each connection this device played on: its latest speed test, the limit
 * the server learned after buffering, and how the latest plan played.
 */
export function DeviceConnections({ device }: { device: AdminDeviceDetail }) {
  const resetLimit = useResetAdminDeviceLearnedLimit();
  if (!device.connections?.length) return null;
  return (
    <section
      aria-labelledby="device-connections-title"
      className="border-border/60 max-h-80 shrink-0 space-y-3 overflow-y-auto border-b px-5 py-4 sm:px-6"
    >
      <h3 id="device-connections-title" className="text-foreground text-sm font-medium">
        Connections
      </h3>
      {device.connections.map((connection) => (
        <div
          key={connection.id}
          className="border-border/60 space-y-1 rounded-md border px-3 py-2.5 text-[13px]"
        >
          <div className="text-foreground font-medium">
            {connectionLabel(connection.connection)}
          </div>
          {connection.probe && (
            <p className="text-muted-foreground">
              {connection.probe.lower_bound && "Faster than "}
              {formatConnectionRateKbps(connection.probe.kbps)} on{" "}
              {TRANSPORT_LABELS[connection.probe.transport] ?? connection.probe.transport} ·
              measured {formatRelative(connection.probe.measured_at)}
            </p>
          )}
          {connection.playback_sample && (
            <p className="text-muted-foreground">
              Faster than {formatConnectionRateKbps(connection.playback_sample.kbps)} · seen during
              playback {formatRelative(connection.playback_sample.measured_at)}
            </p>
          )}
          {connection.learned_limit && (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span>
                Limited to {formatConnectionRateKbps(connection.learned_limit.kbps)} after buffering
                · until {formatDate(connection.learned_limit.expires_at, "medium")}
              </span>
              <Button
                size="sm"
                variant="outline"
                disabled={resetLimit.isPending}
                onClick={() =>
                  resetLimit.mutate({
                    userId: device.user_id,
                    deviceId: device.device_id,
                    connectionId: connection.id,
                  })
                }
              >
                Clear limit
              </Button>
            </div>
          )}
          {connection.plan_summary && (
            <p className="text-muted-foreground">
              Latest playback: {planSummaryText(connection.plan_summary)}
            </p>
          )}
        </div>
      ))}
    </section>
  );
}
