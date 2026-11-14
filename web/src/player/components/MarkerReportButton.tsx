import { Flag } from "lucide-react";
import { useState } from "react";

import { useT, type MessageKey } from "@/i18n";
import {
  parseMarkerReportTarget,
  reportMarkerSegment,
  type MarkerReportResult,
} from "@/lib/markerReport";

interface MarkerReportButtonProps {
  /** The marker plugin installation that serves the report route. */
  installationId: number;
  /** The item's content id, which carries the ids the report is addressed by. */
  contentId: string;
  /** Which marked range the report is about, for the label only. */
  kind?: string;
}

/**
 * The segment names a tooltip can use in a sentence, by marker kind.
 *
 * Named one by one rather than built from the kind, so a kind the player grows
 * without a name shows the tooltip without one instead of an unknown message id.
 */
const KIND_LABELS: Record<string, MessageKey> = {
  intro: "web.markers.kind.intro",
  recap: "web.markers.kind.recap",
  credits: "web.markers.kind.credits",
  preview: "web.markers.kind.preview",
};

type Phase =
  | { state: "idle" }
  | { state: "confirming" }
  | { state: "sending" }
  | { state: "done"; message: string }
  | { state: "failed"; message: string };

/**
 * "Report incorrect segment", drawn under the skip prompt.
 *
 * Two-step on purpose. This is a write against a shared community database and it
 * cannot be taken back from here, so the second click is the confirmation — there is
 * no undo to offer afterwards. It is a plain button next to the pill rather than a
 * dialog because the prompt already owns the viewer's attention and a modal over
 * playing video is a worse interruption than one more line of text.
 *
 * The result replaces the control in place: a report that succeeded says so, a
 * repeat report is not an error (one vote per account per media), and a rejection
 * carries the plugin's own words for what was wrong — that text is the server's and
 * is shown as sent rather than as a second phrasing of the same rule.
 */
export function MarkerReportButton({ installationId, contentId, kind }: MarkerReportButtonProps) {
  const t = useT();
  const [phase, setPhase] = useState<Phase>({ state: "idle" });
  const target = parseMarkerReportTarget(contentId);

  // No addressable target means the control would have nothing to send. Drawing it
  // anyway would produce a failed request for a report the viewer cannot make.
  if (!target) return null;

  const send = async () => {
    setPhase({ state: "sending" });
    const result: MarkerReportResult = await reportMarkerSegment(installationId, target);

    switch (result.status) {
      case "reported":
        setPhase({ state: "done", message: t("web.markers.report.reported") });
        return;
      case "already_reported":
        setPhase({ state: "done", message: t("web.markers.report.alreadyReported") });
        return;
      case "unauthorized":
        setPhase({ state: "failed", message: t("web.markers.report.sessionExpired") });
        return;
      default:
        setPhase({ state: "failed", message: result.message });
    }
  };

  // Names the action for the tooltip, which is where "which segment" is spelled out.
  const kindLabel = kind ? KIND_LABELS[kind] : undefined;
  const label = kindLabel
    ? t("web.markers.report.titleForKind", { kind: t(kindLabel) })
    : t("web.markers.report.title");

  const baseClass =
    "rounded-full border border-white/20 bg-black/75 px-3 py-1.5 text-xs font-medium text-white/85 shadow-2xl backdrop-blur-md hover:border-white/35 hover:bg-black/85 focus-visible:border-white/55 focus-visible:ring-2 focus-visible:ring-white/80 focus-visible:outline-none";

  if (phase.state === "failed") {
    return (
      <button
        type="button"
        onClick={() => setPhase({ state: "idle" })}
        className={`${baseClass} max-w-56 text-start`}
        title={t("web.markers.report.retry")}
      >
        {phase.message}
      </button>
    );
  }

  if (phase.state === "done") {
    return (
      <span
        className={`${baseClass} inline-flex items-center gap-1.5 text-white/60`}
        aria-live="polite"
      >
        <Flag className="h-4 w-4" aria-hidden="true" />
        {phase.message}
      </span>
    );
  }

  if (phase.state === "sending") {
    return (
      <button type="button" disabled className={`${baseClass} text-white/50`}>
        {t("web.markers.report.sending")}
      </button>
    );
  }

  if (phase.state === "confirming") {
    return (
      <span className="flex items-center gap-1.5">
        <button type="button" onClick={send} className={baseClass}>
          {t("web.markers.report.confirm")}
        </button>
        <button
          type="button"
          onClick={() => setPhase({ state: "idle" })}
          className={`${baseClass} text-white/60`}
        >
          {t("common.cancel")}
        </button>
      </span>
    );
  }

  return (
    <button
      type="button"
      onClick={() => setPhase({ state: "confirming" })}
      className={`${baseClass} inline-flex items-center gap-1.5`}
      title={label}
    >
      {/* The same glyph TheIntroDB's own site puts on this action, so the control
          reads as the by-now-familiar one rather than as a new affordance. */}
      <Flag className="h-4 w-4" aria-hidden="true" />
      {t("web.markers.report.action")}
    </button>
  );
}
