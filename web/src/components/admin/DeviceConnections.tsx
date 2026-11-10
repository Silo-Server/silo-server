import type { AdminDeviceDetail } from "@/api/types";
import { formatRelative } from "@/components/admin/deviceOverrides";
import { Button } from "@/components/ui/button";
import { formatConnectionRateKbps } from "@/lib/bitrateOptions";
import { formatDate } from "@/lib/datetime";
import { useResetAdminDeviceLearnedLimit } from "@/hooks/queries/admin/users";
import { useT, type MessageKey, type Translate } from "@/i18n";

type DeviceConnection = NonNullable<AdminDeviceDetail["connections"]>[number];

const TRANSPORT_LABELS: Record<string, MessageKey> = {
  wifi: "web.admin.common.deviceConnections.transport.wifi",
  ethernet: "web.admin.common.deviceConnections.transport.ethernet",
  cellular: "web.admin.common.deviceConnections.transport.cellular",
  other: "web.admin.common.deviceConnections.transport.otherNetwork",
};

/** The last action on the latest plan, as `plan_summary` reports it. */
const BUFFERING_ACTION_LABELS: Record<string, MessageKey> = {
  none: "web.admin.common.deviceConnections.bufferingAction.none",
  ignored: "web.admin.common.deviceConnections.bufferingAction.ignored",
  lowered: "web.admin.common.deviceConnections.bufferingAction.lowered",
  restored: "web.admin.common.deviceConnections.bufferingAction.restored",
  accepted: "web.admin.common.deviceConnections.bufferingAction.accepted",
  kept: "web.admin.common.deviceConnections.bufferingAction.kept",
  timed_out: "web.admin.common.deviceConnections.bufferingAction.timedOut",
  cant_lower: "web.admin.common.deviceConnections.bufferingAction.cantLower",
};

function connectionLabel(t: Translate, connection: string) {
  if (connection === "local") return t("web.admin.common.deviceConnections.local");
  if (connection === "remote") return t("web.admin.common.deviceConnections.remote");
  if (connection.startsWith("provider:")) {
    return t("web.admin.common.deviceConnections.provider", { provider: connection.slice(9) });
  }
  return connection;
}

function transportLabel(t: Translate, transport: string) {
  const key = TRANSPORT_LABELS[transport];
  return key ? t(key) : transport;
}

function planSummaryText(t: Translate, summary: NonNullable<DeviceConnection["plan_summary"]>) {
  const stalls = Number(summary.diagnostics.stall_count ?? 0);
  const seconds = Math.round(Number(summary.diagnostics.stall_total_ms ?? 0) / 1000);
  const action = summary.diagnostics.buffering_action ?? "none";
  const actionKey = BUFFERING_ACTION_LABELS[action];
  return [
    t("web.admin.common.deviceConnections.stalls", { count: stalls, count_text: String(stalls) }),
    t("web.admin.common.deviceConnections.bufferingSeconds", {
      count: seconds,
      count_text: String(seconds),
    }),
    actionKey ? t(actionKey) : action,
    formatRelative(summary.reported_at),
  ].join(" · ");
}

/**
 * Each connection this device played on: its latest speed test, the limit
 * the server learned after buffering, and how the latest plan played.
 */
export function DeviceConnections({ device }: { device: AdminDeviceDetail }) {
  const t = useT();
  const resetLimit = useResetAdminDeviceLearnedLimit();
  if (!device.connections?.length) return null;
  return (
    <section
      aria-labelledby="device-connections-title"
      className="border-border/60 max-h-80 shrink-0 space-y-3 overflow-y-auto border-b px-5 py-4 sm:px-6"
    >
      <h3 id="device-connections-title" className="text-foreground text-sm font-medium">
        {t("web.admin.common.deviceConnections.title")}
      </h3>
      {device.connections.map((connection) => (
        <div
          key={connection.id}
          className="border-border/60 space-y-1 rounded-md border px-3 py-2.5 text-[13px]"
        >
          <div className="text-foreground font-medium">
            {connectionLabel(t, connection.connection)}
          </div>
          {connection.probe && (
            <p className="text-muted-foreground">
              {t("web.admin.common.deviceConnections.probe", {
                lower_bound: connection.probe.lower_bound ? "yes" : "no",
                rate: formatConnectionRateKbps(connection.probe.kbps),
                transport: transportLabel(t, connection.probe.transport),
                measured: formatRelative(connection.probe.measured_at),
              })}
            </p>
          )}
          {connection.playback_sample && (
            <p className="text-muted-foreground">
              {t("web.admin.common.deviceConnections.playbackSample", {
                rate: formatConnectionRateKbps(connection.playback_sample.kbps),
                seen: formatRelative(connection.playback_sample.measured_at),
              })}
            </p>
          )}
          {connection.learned_limit && (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span>
                {t("web.admin.common.deviceConnections.learnedLimit", {
                  rate: formatConnectionRateKbps(connection.learned_limit.kbps),
                  until: formatDate(connection.learned_limit.expires_at, "medium"),
                })}
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
                {t("web.admin.common.deviceConnections.clearLimit")}
              </Button>
            </div>
          )}
          {connection.plan_summary && (
            <p className="text-muted-foreground">
              {t("web.admin.common.deviceConnections.latestPlayback", {
                summary: planSummaryText(t, connection.plan_summary),
              })}
            </p>
          )}
        </div>
      ))}
    </section>
  );
}
