import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { SettingRow } from "@/components/settings/SettingRow";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { formatBitrateKbps } from "@/lib/bitrateOptions";
import { preferredDateLocale } from "@/lib/datetime";
import {
  networkInformation,
  onMeteredConnection,
  ProbeRateLimited,
  testConnectionSpeed,
} from "@/player/bandwidth-probe";
import { toast } from "sonner";

const TRANSPORT_LABELS: Record<string, string> = {
  wifi: "Wi-Fi",
  ethernet: "Ethernet",
  cellular: "cellular",
};

/** "just now", "3 minutes ago", "2 hours ago", "4 days ago". */
function measuredAgo(measuredAt: string, now: number): string {
  const minutes = Math.max(0, Math.floor((now - Date.parse(measuredAt)) / 60_000));
  if (minutes < 1) return "just now";
  const [count, unit] =
    minutes < 60
      ? [minutes, "minute"]
      : minutes < 24 * 60
        ? [Math.floor(minutes / 60), "hour"]
        : [Math.floor(minutes / (24 * 60)), "day"];
  return `${count} ${unit}${count === 1 ? "" : "s"} ago`;
}

/**
 * The server's view of this device on the connection it is using now: the
 * learned limit (buffering_quality_v1) and, with connection_speed_v1, the
 * latest probe and Auto's budget. Both readers share one request.
 */
function usePlaybackConnection() {
  const capabilities = useQuery({
    queryKey: ["playback", "connection-capabilities"],
    queryFn: () => v2("GET /api/v2/playback/capabilities"),
  });
  const connection = useQuery({
    queryKey: ["playback", "connection"],
    queryFn: () => v2("GET /api/v2/playback/connection"),
    enabled: capabilities.data?.features.includes("buffering_quality_v1") === true,
  });
  return {
    connection,
    speedAvailable: capabilities.data?.features.includes("connection_speed_v1") === true,
  };
}

/**
 * "Limited to 44.1 Mbps after buffering on Oct 17", with Reset. Shown under
 * Lower quality when buffering while this connection has a limit.
 */
export function LearnedLimitRow() {
  const { connection } = usePlaybackConnection();
  const [resetting, setResetting] = useState(false);
  const limit = connection.data?.learned_limit;
  if (!limit) return null;
  const observed = new Date(limit.observed_at).toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
  });
  return (
    <SettingRow
      label="Learned limit"
      description={`Limited to ${formatBitrateKbps(limit.kbps)} after buffering on ${observed}`}
      control={() => (
        <Button
          variant="outline"
          disabled={resetting}
          onClick={() => {
            setResetting(true);
            void v2("DELETE /api/v2/playback/connection/learned-limit")
              .then(() => connection.refetch())
              .catch(() => toast.error("Failed to reset learned limit"))
              .finally(() => setResetting(false));
          }}
        >
          Reset
        </Button>
      )}
    />
  );
}

interface SpeedProbe {
  kbps: number;
  lower_bound: boolean;
  measured_at: string;
  transport: string;
}

interface SpeedSample {
  kbps: number;
  measured_at: string;
}

/**
 * The highest reading the server keeps for this connection: the latest test,
 * or the best playback throughput, which is only a lower bound.
 */
function speedDescription(
  connection: { probe?: SpeedProbe | null; playback_sample?: SpeedSample | null } | undefined,
  now: number,
): string | undefined {
  const probe = connection?.probe;
  const sample = connection?.playback_sample;
  if (probe && (!sample || probe.kbps >= sample.kbps)) {
    const rate = probe.lower_bound
      ? `Faster than ${formatBitrateKbps(probe.kbps)}`
      : formatBitrateKbps(probe.kbps);
    return `${rate} · measured ${measuredAgo(probe.measured_at, now)} on ${TRANSPORT_LABELS[probe.transport] ?? "this connection"}`;
  }
  if (sample) {
    return `Faster than ${formatBitrateKbps(sample.kbps)} · seen during playback ${measuredAgo(sample.measured_at, now)}`;
  }
  return undefined;
}

/** The Connection group: this connection's speed, a speed test and Auto's budget. */
export function PlaybackConnection() {
  const { connection, speedAvailable } = usePlaybackConnection();
  const [testing, setTesting] = useState(false);
  const [message, setMessage] = useState<string>();
  const [now, setNow] = useState(Date.now);
  const [metered, setMetered] = useState(onMeteredConnection);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000);
    const network = networkInformation() as EventTarget | undefined;
    const update = () => setMetered(onMeteredConnection());
    network?.addEventListener?.("change", update);
    return () => {
      clearInterval(timer);
      network?.removeEventListener?.("change", update);
    };
  }, []);
  if (!speedAvailable) return null;
  const budget = connection.data?.auto_budget_kbps;
  const description = metered
    ? "Speed tests are skipped on cellular or metered connections. Cellular quality settings apply."
    : (speedDescription(connection.data, now) ?? "Test the connection used to stream videos.");
  return (
    <SettingsGroup title="Connection">
      <SettingRow
        label="Connection speed"
        description={description}
        control={() => (
          <Button
            variant="outline"
            disabled={testing || metered}
            onClick={() => {
              setTesting(true);
              setMessage("Testing for up to 10 seconds.");
              void testConnectionSpeed()
                .then(async (result) => {
                  if (result === "playing")
                    setMessage("Stop playback before testing, then try again.");
                  else if (result === "busy")
                    setMessage("A speed test is already running. Try again in a moment.");
                  else {
                    setMessage(undefined);
                    await connection.refetch();
                  }
                })
                .catch((error: unknown) =>
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
        <p className="text-muted-foreground text-[13px] leading-relaxed" role="status">
          {message}
        </p>
      )}
      {budget !== undefined && !metered && (
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          Auto plays videos up to {formatBitrateKbps(budget)} without converting them.
        </p>
      )}
    </SettingsGroup>
  );
}
