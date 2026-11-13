import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { v2, type V2Result } from "@/api/v2/request";
import { SettingRow } from "@/components/settings/SettingRow";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { formatConnectionRateKbps } from "@/lib/bitrateOptions";
import { preferredDateLocale } from "@/lib/datetime";
import {
  CONNECTION_QUERY_KEY,
  networkInformation,
  ProbeNotSaved,
  ProbeRateLimited,
  testConnectionSpeed,
} from "@/player/bandwidth-probe";

const TRANSPORT_LABELS: Record<string, MessageKey> = {
  wifi: "web.settings.playbackConnection.transport.wifi",
  ethernet: "web.settings.playbackConnection.transport.ethernet",
  cellular: "web.settings.playbackConnection.transport.cellular",
};

/** "just now", "3 minutes ago", "2 hours ago", "4 days ago". */
function measuredAgo(t: Translate, measuredAt: string, now: number): string {
  const minutes = Math.max(0, Math.floor((now - Date.parse(measuredAt)) / 60_000));
  if (minutes < 1) return t("web.settings.playbackConnection.ago.justNow");
  if (minutes < 60)
    return t("web.settings.playbackConnection.ago.minutes", {
      count: minutes,
      count_text: minutes,
    });
  if (minutes < 24 * 60) {
    const hours = Math.floor(minutes / 60);
    return t("web.settings.playbackConnection.ago.hours", {
      count: hours,
      count_text: hours,
    });
  }
  const days = Math.floor(minutes / (24 * 60));
  return t("web.settings.playbackConnection.ago.days", { count: days, count_text: days });
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
  const t = useT();
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
        label={t("web.settings.playbackConnection.learnedLimit", {
          rate: formatConnectionRateKbps(limit.kbps),
          date: observed,
        })}
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
            {t("web.settings.playbackConnection.reset")}
          </Button>
        )}
      />
      {failed && (
        <p className="text-muted-foreground text-[13px] leading-relaxed" role="status">
          {t("web.settings.playbackConnection.resetFailed")}
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
  t: Translate,
  connection: ConnectionState | undefined,
  now: number,
): string | undefined {
  const probe = connection?.probe;
  const sample = connection?.playback_sample;
  // A playback sample stands in only without a probe: a fresh test result
  // must not hide behind an older, higher sample.
  if (probe) {
    const rate = probe.lower_bound
      ? t("web.settings.playbackConnection.fasterThan", {
          rate: formatConnectionRateKbps(probe.kbps),
        })
      : formatConnectionRateKbps(probe.kbps);
    const transport = TRANSPORT_LABELS[probe.transport];
    return t("web.settings.playbackConnection.measured", {
      rate,
      ago: measuredAgo(t, probe.measured_at, now),
      transport: transport ? t(transport) : t("web.settings.playbackConnection.transport.unknown"),
    });
  }
  if (sample) {
    return t("web.settings.playbackConnection.seenDuringPlayback", {
      rate: formatConnectionRateKbps(sample.kbps),
      ago: measuredAgo(t, sample.measured_at, now),
    });
  }
  return undefined;
}

/**
 * What replaces the speed on a link that is not tested: cellular, where the
 * browser reports it, or a metered link (Data Saver), in the spec's words.
 */
function meteredNote(): MessageKey | undefined {
  const network = networkInformation();
  if (network?.type === "cellular") return "web.settings.playbackConnection.skippedCellular";
  if (network?.saveData === true) return "web.settings.playbackConnection.skippedMetered";
  return undefined;
}

/**
 * The Connection group: this connection's speed, a speed test and Auto's
 * budget. `ignoresLearnedLimit` is Never mode, whose playback ignores the
 * learned limit that the server's budget includes.
 */
export function PlaybackConnection({ ignoresLearnedLimit = false }) {
  const t = useT();
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
  const description =
    (note && t(note)) ??
    speedDescription(t, connection.data, now) ??
    t("web.settings.playbackConnection.notMeasured");
  const say = (text: string, forMs?: number) => setMessage({ text, forMs });
  return (
    <SettingsGroup title={t("web.settings.playbackConnection.title")}>
      <SettingRow
        label={t("web.settings.playbackConnection.speed")}
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
                    if (result === "playing")
                      say(t("web.settings.playbackConnection.stopPlayback"));
                    else if (result === "busy") say(t("web.settings.playbackConnection.busy"));
                    // The link turned metered: the row already shows why.
                    else if (result === "metered") setNote(meteredNote());
                    else await connection.refetch();
                  })
                  .catch((error: unknown) => {
                    if (error instanceof ProbeRateLimited)
                      say(
                        t("web.settings.playbackConnection.rateLimited", {
                          count: error.retryAfter,
                          count_text: error.retryAfter,
                        }),
                        error.retryAfter * 1000,
                      );
                    else if (error instanceof ProbeNotSaved)
                      say(t("web.settings.playbackConnection.notSaved"));
                    else say(t("web.settings.playbackConnection.testFailed"));
                  })
                  .finally(() => setTesting(false));
              }}
            >
              {testing
                ? t("web.settings.playbackConnection.testing")
                : t("web.settings.playbackConnection.test")}
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
          {t("web.settings.playbackConnection.autoBudget", {
            rate: formatConnectionRateKbps(budget),
          })}
        </p>
      )}
    </SettingsGroup>
  );
}
