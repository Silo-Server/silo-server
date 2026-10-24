import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { v2, type V2Result } from "@/api/v2/request";
import { SettingRow } from "@/components/settings/SettingRow";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { formatConnectionRateKbps } from "@/lib/bitrateOptions";
import { preferredDateLocale } from "@/lib/datetime";
import {
  CONNECTION_QUERY_KEY,
  networkInformation,
  ProbeNotSaved,
  ProbeRateLimited,
  testConnectionSpeed,
} from "@/player/bandwidth-probe";

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
 * latest probe and Auto's budget. Both readers share one request, read afresh
 * whenever the page opens. The probe schedule shares the query too: a read it
 * makes updates the page, and a new network invalidates it, since a limit or
 * speed shown from before the change belongs to the network left behind.
 */
function usePlaybackConnection() {
  const capabilities = useQuery({
    queryKey: ["playback", "connection-capabilities"],
    queryFn: () => v2("GET /api/v2/playback/capabilities"),
  });
  const connection = useQuery({
    queryKey: CONNECTION_QUERY_KEY,
    queryFn: ({ signal }) => v2("GET /api/v2/playback/connection", { signal }),
    enabled: capabilities.data?.features.includes("buffering_quality_v1") === true,
    staleTime: 0,
  });
  return {
    connection,
    speedAvailable: capabilities.data?.features.includes("connection_speed_v1") === true,
  };
}

/**
 * "Limited to 44 Mbps after buffering on Oct 17", with Reset. Shown under
 * Lower quality when buffering while this connection has a limit.
 */
export function LearnedLimitRow() {
  const { connection } = usePlaybackConnection();
  const [resetting, setResetting] = useState(false);
  const [failed, setFailed] = useState(false);
  const limit = connection.data?.learned_limit;
  if (!limit) return null;
  const observed = new Date(limit.observed_at).toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
  });
  return (
    <>
      <SettingRow
        label={`Limited to ${formatConnectionRateKbps(limit.kbps)} after buffering on ${observed}`}
        control={() => (
          <Button
            variant="outline"
            disabled={resetting}
            onClick={() => {
              setResetting(true);
              setFailed(false);
              void v2("DELETE /api/v2/playback/connection/learned-limit")
                .then(() => connection.refetch())
                .catch(() => setFailed(true))
                .finally(() => setResetting(false));
            }}
          >
            Reset
          </Button>
        )}
      />
      {failed && (
        <p className="text-muted-foreground text-[13px] leading-relaxed" role="status">
          Couldn't reset the limit. Try again.
        </p>
      )}
    </>
  );
}

type ConnectionState = V2Result<"GET /api/v2/playback/connection">;

/**
 * The highest reading the server keeps for this connection: the latest test,
 * or the best playback throughput, which is only a lower bound.
 */
function speedDescription(
  connection: ConnectionState | undefined,
  now: number,
): string | undefined {
  const probe = connection?.probe;
  const sample = connection?.playback_sample;
  // A playback sample stands in only without a probe: a fresh test result
  // must not hide behind an older, higher sample.
  if (probe) {
    const rate = probe.lower_bound
      ? `Faster than ${formatConnectionRateKbps(probe.kbps)}`
      : formatConnectionRateKbps(probe.kbps);
    return `${rate} · measured ${measuredAgo(probe.measured_at, now)} on ${TRANSPORT_LABELS[probe.transport] ?? "this connection"}`;
  }
  if (sample) {
    return `Faster than ${formatConnectionRateKbps(sample.kbps)} · seen during playback ${measuredAgo(sample.measured_at, now)}`;
  }
  return undefined;
}

/**
 * What replaces the speed on a link that is not tested: cellular, where the
 * browser reports it, or a metered link (Data Saver), in the spec's words.
 */
function meteredNote(): string | undefined {
  const network = networkInformation();
  if (network?.type === "cellular")
    return "Speed tests are skipped on cellular, and the Cellular quality settings apply.";
  if (network?.saveData === true) return "Speed tests are skipped on metered networks.";
  return undefined;
}

/**
 * The Connection group: this connection's speed, a speed test and Auto's
 * budget. `ignoresLearnedLimit` is Never mode, whose playback ignores the
 * learned limit that the server's budget includes.
 */
export function PlaybackConnection({ ignoresLearnedLimit = false }) {
  const { connection, speedAvailable } = usePlaybackConnection();
  const [testing, setTesting] = useState(false);
  // A message shown for `forMs` hides itself then.
  const [message, setMessage] = useState<{ text: string; forMs?: number }>();
  const [now, setNow] = useState(Date.now);
  const [note, setNote] = useState(meteredNote);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000);
    const network = networkInformation() as EventTarget | undefined;
    const update = () => setNote(meteredNote());
    network?.addEventListener?.("change", update);
    return () => {
      clearInterval(timer);
      network?.removeEventListener?.("change", update);
    };
  }, []);
  // "Try again in N seconds" is only true for those N seconds.
  useEffect(() => {
    if (message?.forMs === undefined) return;
    const timer = setTimeout(() => setMessage(undefined), message.forMs);
    return () => clearTimeout(timer);
  }, [message]);
  if (!speedAvailable) return null;
  const budget =
    ignoresLearnedLimit && connection.data?.learned_limit
      ? undefined
      : connection.data?.auto_budget_kbps;
  const description = note ?? speedDescription(connection.data, now) ?? "Not measured yet";
  const say = (text: string, forMs?: number) => setMessage({ text, forMs });
  return (
    <SettingsGroup title="Connection">
      <SettingRow
        label="Connection speed"
        description={description}
        control={() =>
          note ? null : (
            <Button
              variant="outline"
              disabled={testing}
              onClick={() => {
                setTesting(true);
                setMessage(undefined);
                void testConnectionSpeed()
                  .then(async (result) => {
                    if (result === "playing") say("Stop playback before testing, then try again.");
                    else if (result === "busy")
                      say("A speed test is already running. Try again in a moment.");
                    // The link turned metered: the row already shows why.
                    else if (result === "metered") setNote(meteredNote());
                    else await connection.refetch();
                  })
                  .catch((error: unknown) => {
                    if (error instanceof ProbeRateLimited)
                      say(error.message, error.retryAfter * 1000);
                    else if (error instanceof ProbeNotSaved)
                      say("Couldn't save the speed test. Try again.");
                    else say("Couldn't measure the connection speed. Try again.");
                  })
                  .finally(() => setTesting(false));
              }}
            >
              {testing ? "Testing…" : "Test speed"}
            </Button>
          )
        }
      />
      {message && (
        <p className="text-muted-foreground text-[13px] leading-relaxed" role="status">
          {message.text}
        </p>
      )}
      {budget !== undefined && !note && (
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          Auto plays videos up to {formatConnectionRateKbps(budget)} without converting them.
        </p>
      )}
    </SettingsGroup>
  );
}
