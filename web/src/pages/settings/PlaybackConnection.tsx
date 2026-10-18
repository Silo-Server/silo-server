import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { SettingRow } from "@/components/settings/SettingRow";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import {
  networkInformation,
  onMeteredConnection,
  ProbeRateLimited,
  testConnectionSpeed,
} from "@/player/bandwidth-probe";
import { toast } from "sonner";

const connectionMbps = (kbps: number) =>
  `${(kbps / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })} Mbps`;
export function PlaybackConnection() {
  const [testing, setTesting] = useState(false);
  const [now, setNow] = useState(Date.now);
  const [metered, setMetered] = useState(onMeteredConnection);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60000);
    const network = networkInformation() as EventTarget | undefined;
    const update = () => setMetered(onMeteredConnection());
    network?.addEventListener?.("change", update);
    return () => {
      clearInterval(timer);
      network?.removeEventListener?.("change", update);
    };
  }, []);
  const [message, setMessage] = useState<string>();
  const capabilities = useQuery({
    queryKey: ["playback", "connection-capabilities"],
    queryFn: () => v2("GET /api/v2/playback/capabilities"),
  });
  const connection = useQuery({
    queryKey: ["playback", "connection"],
    queryFn: () => v2("GET /api/v2/playback/connection"),
    enabled: capabilities.data?.features.includes("buffering_quality_v1") === true,
  });
  const limit = connection.data?.learned_limit;
  const speedAvailable = capabilities.data?.features.includes("connection_speed_v1");
  const probe = connection.data?.probe;
  const budget = connection.data?.auto_budget_kbps;
  if (!limit && !speedAvailable) return null;
  const transport = {
    wifi: "Wi-Fi",
    ethernet: "Ethernet",
    cellular: "cellular",
    other: "this connection",
  };
  const minutes = probe
    ? Math.max(0, Math.floor((now - Date.parse(probe.measured_at)) / 60000))
    : 0;
  const description = metered
    ? "Speed tests are skipped on cellular or metered connections. Cellular quality settings apply."
    : probe
      ? `${connectionMbps(probe.kbps)} · measured ${minutes < 1 ? "just now" : `${minutes} minute${minutes === 1 ? "" : "s"} ago`} on ${transport[probe.transport as keyof typeof transport] ?? "this connection"}`
      : "Test the connection used to stream videos.";
  return (
    <SettingsGroup title="Connection">
      {speedAvailable && (
        <>
          <SettingRow
            label="Connection speed"
            description={description}
            control={() => (
              <Button
                variant="outline"
                disabled={testing || metered}
                onClick={() => {
                  setTesting(true);
                  setMessage(undefined);
                  void testConnectionSpeed()
                    .then(async (result) => {
                      if (result === undefined)
                        setMessage("Stop playback before testing, then try again.");
                      else await connection.refetch();
                    })
                    .catch((error) =>
                      setMessage(
                        error instanceof ProbeRateLimited
                          ? error.message
                          : "Could not measure connection speed. Try again.",
                      ),
                    )
                    .finally(() => setTesting(false));
                }}
              >
                {testing ? "Testing…" : "Test speed"}
              </Button>
            )}
          />
          {message && (
            <p className="text-muted-foreground px-4 py-2 text-sm" role="status">
              {message}
            </p>
          )}
          {budget !== undefined && !metered && (
            <p className="text-muted-foreground px-4 py-2 text-sm">
              Auto plays videos up to {connectionMbps(budget)} without converting them.
            </p>
          )}
        </>
      )}
      {limit && (
        <SettingRow
          label="Learned limit"
          description={`Limited to ${connectionMbps(limit.kbps)} after buffering on ${new Date(limit.observed_at).toLocaleDateString()}`}
          control={() => (
            <Button
              variant="outline"
              onClick={() => {
                void v2("DELETE /api/v2/playback/connection/learned-limit")
                  .then(() => connection.refetch())
                  .catch(() => toast.error("Failed to reset learned limit"));
              }}
            >
              Reset
            </Button>
          )}
        />
      )}
    </SettingsGroup>
  );
}
