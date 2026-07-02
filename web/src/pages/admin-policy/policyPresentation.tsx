import { Eye, KeyRound, MonitorPlay, ScrollText, type LucideIcon } from "lucide-react";

import type { PolicyDocument } from "@/api/types";
import { cn } from "@/lib/utils";

import { formatPolicyDomain } from "./policyPageUtils";

export interface PolicyDomainMeta {
  icon: LucideIcon;
  title: string;
  /** Plain-language summary of what this domain governs. */
  governs: string;
  /** A concrete example of a narrowing rule an admin might write. */
  example: string;
}

const DOMAIN_META: Record<string, PolicyDomainMeta> = {
  scope: {
    icon: Eye,
    title: "Library visibility",
    governs:
      "What each profile can see: allowed libraries, the content-rating ceiling, and the playback-quality ceiling. Evaluated on every signed-in request.",
    example: "“After 21:00, cap every profile at PG.”",
  },
  permission: {
    icon: KeyRound,
    title: "Admin & permissions",
    governs:
      "The acting-admin gate and per-user permissions such as marker editing and metadata curation.",
    example: "“Deny metadata curation on weekends.”",
  },
  action: {
    icon: MonitorPlay,
    title: "Downloads & playback",
    governs:
      "Download and transcode eligibility, plus how many concurrent streams and transcodes a user may run.",
    example: "“No downloads between 22:00 and 07:00.”",
  },
};

const FALLBACK_META: PolicyDomainMeta = {
  icon: ScrollText,
  title: "Policy",
  governs: "Custom decisions for this domain.",
  example: "",
};

export function policyDomainMeta(domain: string): PolicyDomainMeta {
  return DOMAIN_META[domain] ?? { ...FALLBACK_META, title: formatPolicyDomain(domain) };
}

export type PolicyDocumentStatus = "live" | "draft" | "disabled";

export function policyDocumentStatus(document: PolicyDocument): PolicyDocumentStatus {
  if (!document.enabled) return "disabled";
  return document.active_version_id ? "live" : "draft";
}

const STATUS_PRESENTATION: Record<PolicyDocumentStatus, { label: string; dot: string }> = {
  live: { label: "Live", dot: "bg-emerald-400" },
  draft: { label: "Draft", dot: "bg-amber-400" },
  disabled: { label: "Disabled", dot: "bg-muted-foreground/50" },
};

interface PolicyStatusPillProps {
  status: PolicyDocumentStatus;
  /** Version number to show next to a live status, when known. */
  versionNumber?: number;
  className?: string;
}

export function PolicyStatusPill({ status, versionNumber, className }: PolicyStatusPillProps) {
  const presentation = STATUS_PRESENTATION[status];
  return (
    <span
      className={cn(
        "border-border text-foreground/80 inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium",
        className,
      )}
    >
      <span aria-hidden className={cn("size-1.5 rounded-full", presentation.dot)} />
      {presentation.label}
      {status === "live" && versionNumber !== undefined && (
        <span className="text-muted-foreground">v{versionNumber}</span>
      )}
    </span>
  );
}
