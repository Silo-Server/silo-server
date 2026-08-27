import type { AdminLiveSessionsResponse, AdminSession } from "@/api/types";
import { formatFileSize, formatMbpsFromKbps } from "@/lib/mediaFormat";

/**
 * What to render for one session's measured delivery.
 *
 * `null` only when the server has telemetry switched off, in which case no row
 * carries a telemetry block at all. Every session in the merged view has one,
 * because every process publishes into that view — including the session manager
 * that knows about sessions nothing has delivered a byte for.
 */
export interface SessionDeliveryDisplay {
  /** Bytes delivered to the viewer, e.g. "1.8 GB". */
  bytes: string;
  /**
   * Delivery rate, e.g. "12.4 Mbps", or null when it has not been measured yet.
   * A session is only rated once it has been seen in two consecutive view
   * builds, and "not yet measured" must never render as 0 — that reads as a
   * stalled stream on a session that is streaming fine.
   */
  rate: string | null;
  /**
   * True when a client reports this as playing but nothing was measured leaving
   * the server. Paused sessions are never marked: a paused client legitimately
   * stops pulling bytes.
   */
  noDelivery: boolean;
  /**
   * Bytes went out, but no session manager claims this session. Gated while the
   * merged view is incomplete or the session is inside the delivery grace window,
   * because either condition can temporarily hide the reporting publisher.
   */
  unclaimed: boolean;
  /** The byte total is a known floor, not a measurement. */
  degraded: boolean;
  /** Publishers disagreed about who is watching. Worth showing, not resolving. */
  identityConflict: boolean;
  /** More than one address pulled bytes for this session. */
  viewerIpCount: number;
}

/**
 * Mirrors `noDeliveryGrace` in internal/api/handlers/admin_live_sessions.go. A session is
 * reported the moment playback starts, but nothing has ASKED for a byte yet; measuring it
 * takes a client request, a sweep and a merge. Without this window every ordinary start
 * would render as an anomaly for its first seconds.
 */
export const SESSION_DELIVERY_GRACE_MS = 30_000;

export interface SessionDeliveryContext {
  /**
   * The envelope's `view_complete`. An incomplete view is blindness, not disagreement: the
   * publisher holding this session's bytes may be exactly the one that is missing.
   * Omitted means "nothing is known to be missing" — the six pure-helper call sites that
   * pass no context keep their existing behavior.
   */
  viewComplete?: boolean;
  /** Reading instant. Injectable so tests do not depend on wall-clock. Defaults to Date.now(). */
  now?: number;
}

function parseSessionStartedAt(startedAt: string | undefined): number | null {
  if (!startedAt) return null;
  const parsed = Date.parse(startedAt);
  return Number.isNaN(parsed) || parsed <= 0 ? null : parsed;
}

export function describeSessionDelivery(
  session: AdminSession,
  context: SessionDeliveryContext = {},
): SessionDeliveryDisplay | null {
  const telemetry = session.telemetry;
  if (!telemetry) return null;
  const now = context.now ?? Date.now();
  const startedAtMs = parseSessionStartedAt(session.started_at);
  const withinGrace = startedAtMs !== null && now - startedAtMs < SESSION_DELIVERY_GRACE_MS;
  const blind = context.viewComplete === false;
  return {
    bytes: formatFileSize(telemetry.viewer_bytes, { fallback: "0 B" }),
    rate:
      typeof telemetry.delivery_rate_kbps === "number"
        ? formatMbpsFromKbps(telemetry.delivery_rate_kbps, "0.0 Mbps")
        : null,
    noDelivery: telemetry.no_delivery === true,
    unclaimed: telemetry.evidence === "measured" && !blind && !withinGrace,
    degraded: telemetry.bytes_degraded === true,
    identityConflict: telemetry.identity_conflict === true,
    viewerIpCount: telemetry.viewer_ips?.length ?? 0,
  };
}

/**
 * How the live-session list should describe itself.
 *
 * There is no "is this telemetry-backed or legacy?" question: the merged view is
 * the only source, and every process publishes into it. What is left is how much
 * of the fleet the view can currently see, and how many sessions were held back.
 */
export interface LiveSessionsSourceDisplay {
  /** Short badge text, or "" when there is nothing worth saying. */
  label: string;
  /** The full explanation, for a tooltip. Empty when there is nothing to explain. */
  detail: string;
  /** The view is complete and fresh, so the list can be read at face value. */
  trustworthy: boolean;
  /** Offer the reveal control only when there is something hidden to reveal. */
  canRevealHidden: boolean;
  hiddenCount: number;
}

export function describeLiveSessionsSource(
  response: AdminLiveSessionsResponse | undefined,
): LiveSessionsSourceDisplay {
  if (!response) {
    return { label: "", detail: "", trustworthy: false, canRevealHidden: false, hiddenCount: 0 };
  }
  const hiddenCount = response.no_delivery_count ?? 0;
  // The reveal control stays available once the rows are shown, so the toggle
  // can be turned back off.
  const canRevealHidden = hiddenCount > 0 || response.no_delivery_shown;

  if (!response.telemetry_enabled) {
    return {
      label: "reported",
      detail:
        "Stream telemetry is off, so this is what clients report about themselves. " +
        "Nothing here has been checked against bytes actually delivered.",
      trustworthy: false,
      // Nothing was filtered, so there is nothing held back to reveal.
      canRevealHidden: false,
      hiddenCount: 0,
    };
  }
  if (!response.view_available) {
    return {
      label: "starting up",
      detail:
        "The merged telemetry view has not been built yet, so this is the last " +
        "known session list rather than a measured one.",
      trustworthy: false,
      canRevealHidden: false,
      hiddenCount: 0,
    };
  }
  if (!response.view_complete) {
    return {
      label: "partial",
      detail: [
        "Some publishers are missing, so sessions they serve may be absent or " + "under-counted.",
        ...(response.incomplete_reasons ?? []),
      ].join(" "),
      trustworthy: false,
      canRevealHidden,
      hiddenCount,
    };
  }
  if (response.view_stale) {
    // Complete, but the last refresh failed or is still in flight. Totals and
    // membership may both be obsolete, so this is not the same claim as a fresh
    // measurement even though the underlying view was complete when it was built.
    return {
      label: "measured (stale)",
      detail:
        "Measured delivery, but the merged view could not be refreshed, so totals " +
        "and the session list may both be out of date.",
      trustworthy: false,
      canRevealHidden,
      hiddenCount,
    };
  }
  return {
    label: "measured",
    detail: "Measured delivery, corroborated across every publisher.",
    trustworthy: true,
    canRevealHidden,
    hiddenCount,
  };
}
