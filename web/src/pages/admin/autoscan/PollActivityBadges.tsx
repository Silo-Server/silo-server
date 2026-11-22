import type {
  AutoscanEvent,
  AutoscanEventChange,
  AutoscanEventStatus,
  AutoscanScanResult,
  AutoscanScanStatus,
  Library,
  ScanRun,
} from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

import {
  CHANGE_OUTCOME_TONES,
  SCAN_STATUS_LABELS,
  changeReasonText,
  changeScanKeys,
  changeScopeLabel,
  eventStatusTone,
  isKnownChangeOutcome,
  libraryName,
  rawCountValues,
  scanStatusClass,
  splitDisplayPath,
  summarizeScanResult,
} from "./pollEventDisplay";

export function PollStatusBadge({ status }: { status: AutoscanEventStatus }) {
  const t = useT();
  const tone = eventStatusTone(status);
  const Icon = tone.icon;
  return (
    <Badge variant="outline" className={tone.className}>
      <Icon className={cn("h-3.5 w-3.5", status === "running" && "animate-spin")} />
      {t(tone.label)}
    </Badge>
  );
}

export function ScanStatusBadge({ status }: { status: AutoscanScanStatus | ScanRun["status"] }) {
  const t = useT();
  return (
    <Badge variant="outline" className={cn("capitalize tabular-nums", scanStatusClass(status))}>
      {Object.hasOwn(SCAN_STATUS_LABELS, status) ? t(SCAN_STATUS_LABELS[status]) : status}
    </Badge>
  );
}

/**
 * Marks webhook-delivered events (with the arr event type when known) so they
 * read distinctly from poll cycles. Poll events render nothing — poll is the
 * default and labeling every row would be noise.
 */
export function DeliveryBadge({ event }: { event: AutoscanEvent }) {
  const t = useT();
  if (event.delivery_mode !== "webhook") return null;
  return (
    <Badge variant="secondary" className="text-xs">
      {event.provider_event_type
        ? t("web.admin.autoscan.activity.delivery.webhookEvent", {
            event_type: event.provider_event_type,
          })
        : t("web.admin.autoscan.activity.delivery.webhook")}
    </Badge>
  );
}

export function ScanResultText({
  result,
  className,
}: {
  result: AutoscanScanResult | undefined;
  className?: string;
}) {
  const t = useT();
  const summary = summarizeScanResult(t, result);
  if (!summary) return null;
  return (
    <span
      className={cn(
        "text-xs tabular-nums",
        summary.tone === "skipped" && "text-amber-500",
        summary.tone === "errors" && "text-destructive",
        summary.tone === "changes" && "text-foreground",
        summary.tone === "none" && "text-muted-foreground",
        className,
      )}
    >
      {summary.text}
    </span>
  );
}

export function PathDisplay({ path, className }: { path: string; className?: string }) {
  const { dir, leaf } = splitDisplayPath(path);
  return (
    <div
      className={cn(
        "font-mono text-xs leading-relaxed [overflow-wrap:anywhere] whitespace-normal",
        className,
      )}
    >
      {dir ? <span className="text-muted-foreground">{dir}</span> : null}
      <span className="text-foreground font-medium">{leaf}</span>
    </div>
  );
}

export function PollMetricStrip({
  event,
  variant = "inline",
}: {
  event: AutoscanEvent;
  variant?: "inline" | "pills";
}) {
  const t = useT();
  const items = [
    {
      key: "changes",
      label: t(
        "web.admin.autoscan.activity.metrics.changes",
        rawCountValues(event.changes_returned),
      ),
      inlineTone:
        event.changes_returned > 0 ? "text-foreground font-medium" : "text-muted-foreground/60",
      pillTone:
        event.changes_returned > 0
          ? "border-border bg-muted/40 text-foreground font-medium"
          : "border-border/50 text-muted-foreground/60",
    },
    {
      key: "targets",
      label: t(
        "web.admin.autoscan.activity.metrics.targets",
        rawCountValues(event.targets_claimed),
      ),
      inlineTone: event.targets_claimed > 0 ? "text-foreground" : "text-muted-foreground/60",
      pillTone:
        event.targets_claimed > 0
          ? "border-border bg-muted/40 text-foreground"
          : "border-border/50 text-muted-foreground/60",
    },
    {
      key: "created",
      label: t("web.admin.autoscan.activity.metrics.created", rawCountValues(event.scans_created)),
      inlineTone:
        event.scans_created > 0 ? "text-emerald-500 font-medium" : "text-muted-foreground/60",
      pillTone:
        event.scans_created > 0
          ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-500 font-medium"
          : "border-border/50 text-muted-foreground/60",
    },
    {
      key: "reused",
      label: t("web.admin.autoscan.activity.metrics.reused", rawCountValues(event.scans_reused)),
      inlineTone: event.scans_reused > 0 ? "text-sky-500" : "text-muted-foreground/60",
      pillTone:
        event.scans_reused > 0
          ? "border-sky-500/30 bg-sky-500/10 text-sky-500"
          : "border-border/50 text-muted-foreground/60",
    },
    {
      key: "suppressed",
      label: t(
        "web.admin.autoscan.activity.metrics.suppressed",
        rawCountValues(event.scans_suppressed),
      ),
      inlineTone: event.scans_suppressed > 0 ? "text-amber-500" : "text-muted-foreground/60",
      pillTone:
        event.scans_suppressed > 0
          ? "border-amber-500/30 bg-amber-500/10 text-amber-500"
          : "border-border/50 text-muted-foreground/60",
    },
  ];

  if (variant === "pills") {
    return (
      <div className="flex flex-wrap gap-1.5 tabular-nums">
        {items.map((item) => (
          <span
            key={item.key}
            className={cn(
              "inline-flex items-center rounded-md border px-2 py-0.5 text-xs",
              item.pillTone,
            )}
          >
            {item.label}
          </span>
        ))}
      </div>
    );
  }

  return (
    <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-xs tabular-nums">
      {items.map((item) => (
        <span key={item.key} className={item.inlineTone}>
          {item.label}
        </span>
      ))}
    </div>
  );
}

// An outcome added by a newer server shows its raw value in a neutral badge.
export function ChangeOutcomeBadge({ outcome }: { outcome: AutoscanEventChange["outcome"] }) {
  const t = useT();
  const tone = isKnownChangeOutcome(outcome)
    ? { ...CHANGE_OUTCOME_TONES[outcome], label: t(CHANGE_OUTCOME_TONES[outcome].label) }
    : { label: outcome, className: "border-muted-foreground/25 bg-muted/60 text-muted-foreground" };
  return (
    <Badge variant="outline" className={cn("whitespace-nowrap", tone.className)}>
      {tone.label}
    </Badge>
  );
}

export function ChangeExplanation({
  change,
  librariesByID,
}: {
  change: AutoscanEventChange;
  librariesByID: Map<number, Library>;
}) {
  const t = useT();
  const library = change.library_id ? libraryName(t, librariesByID, change.library_id) : null;
  switch (change.outcome) {
    case "queued":
    case "joined": {
      const [plain, inLibrary] = changeScanKeys(change);
      const scope = changeScopeLabel(t, change);
      return (
        <div className="text-muted-foreground text-xs [overflow-wrap:anywhere]">
          {library ? t(inLibrary, { scope, library }) : t(plain, { scope })}
        </div>
      );
    }
    case "suppressed":
      return (
        <div className="text-muted-foreground text-xs [overflow-wrap:anywhere]">
          {change.target_path
            ? t("web.admin.autoscan.activity.change.suppressedPath", { path: change.target_path })
            : t("web.admin.autoscan.activity.change.suppressed")}
        </div>
      );
    case "error":
      return (
        <div className="text-destructive text-xs [overflow-wrap:anywhere]">
          {change.reason === "resolve_failed"
            ? change.detail
              ? t("web.admin.autoscan.activity.change.resolveFailedDetail", {
                  detail: change.detail,
                })
              : t("web.admin.autoscan.activity.change.resolveFailed")
            : (changeReasonText(t, change.reason) ??
              change.detail ??
              t("web.admin.autoscan.activity.change.processingFailed"))}
        </div>
      );
    default: {
      const text =
        changeReasonText(t, change.reason) ??
        change.detail ??
        change.reason ??
        t("web.admin.autoscan.activity.change.notScanned");
      return (
        <div
          className={cn(
            "text-xs [overflow-wrap:anywhere]",
            change.outcome === "unresolved" ? "text-amber-500" : "text-muted-foreground",
          )}
        >
          {library ? t("web.admin.autoscan.activity.change.withLibrary", { text, library }) : text}
        </div>
      );
    }
  }
}
