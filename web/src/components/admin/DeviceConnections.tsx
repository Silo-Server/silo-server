import { useState } from "react";
import { v2 } from "@/api/v2/request";
import type { AdminDeviceDetail } from "@/api/types";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";

const mbps = (kbps: number) =>
  `${(kbps / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })} Mbps`;
export function DeviceConnections({
  device,
  refresh,
}: {
  device: AdminDeviceDetail;
  refresh: () => void;
}) {
  const [resetting, setResetting] = useState<string>();
  if (!device.connections?.length) return null;
  return (
    <section
      className="max-h-80 shrink-0 space-y-3 overflow-y-auto border-b p-4"
      aria-label="Connections"
    >
      <h3 className="text-sm font-medium">Connections</h3>
      {device.connections.map((connection) => (
        <div key={connection.id} className="space-y-1 rounded-md border p-3 text-sm">
          <div className="font-medium">
            {connection.connection === "local"
              ? "Local network"
              : connection.connection === "remote"
                ? "Remote connection"
                : connection.connection}
          </div>
          {connection.probe && (
            <p className="text-muted-foreground">
              {mbps(connection.probe.kbps)} ·{" "}
              {connection.probe.transport === "wifi" ? "Wi-Fi" : connection.probe.transport} ·{" "}
              {new Date(connection.probe.measured_at).toLocaleString()}
            </p>
          )}
          {connection.learned_limit && (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span>Limited to {mbps(connection.learned_limit.kbps)} after buffering</span>
              <Button
                size="sm"
                variant="outline"
                disabled={!!resetting}
                onClick={() => {
                  setResetting(connection.id);
                  void v2(
                    "DELETE /api/v2/admin/devices/{user_id}/{device_id}/connections/{connection_id}/learned-limit",
                    {
                      path: {
                        user_id: String(device.user_id),
                        device_id: device.device_id,
                        connection_id: connection.id,
                      },
                    },
                  )
                    .then(refresh)
                    .catch(() => toast.error("Failed to clear learned limit"))
                    .finally(() => setResetting(undefined));
                }}
              >
                Clear limit
              </Button>
            </div>
          )}
          {connection.plan_summary && (
            <p className="text-muted-foreground">
              Latest playback: {connection.plan_summary.diagnostics.stall_count ?? "0"} stalls ·{" "}
              {Math.round(Number(connection.plan_summary.diagnostics.stall_total_ms ?? 0) / 1000)}{" "}
              seconds buffering ·{" "}
              {connection.plan_summary.diagnostics.buffering_action?.replaceAll("_", " ") ?? "none"}
            </p>
          )}
        </div>
      ))}
    </section>
  );
}
