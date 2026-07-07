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
  /**
   * Plain-language statements of what the shipped baseline enforces. These
   * summarize the vendor Rego for the Baseline tab; the module source next to
   * them remains the truth.
   */
  baseline: string[];
  /** What an override for this domain is allowed to change. */
  overrideCan: string;
}

const DOMAIN_META: Record<string, PolicyDomainMeta> = {
  scope: {
    icon: Eye,
    title: "Library visibility",
    governs:
      "What each profile can see: allowed libraries, the content-rating ceiling, and the playback-quality ceiling. Evaluated on every signed-in request.",
    example: "“After 21:00, cap every profile at PG.”",
    baseline: [
      "A profile sees only the libraries allowed for both the account and the profile — whichever of the two restricts, applies.",
      "Libraries a user hid for themselves stay hidden.",
      "The profile's content-rating ceiling hides anything rated above it (see the rating tiers below).",
      "Playback quality is capped at the stricter of the account and profile ceilings.",
      "A profile protected by a PIN must be verified before it sees anything.",
    ],
    overrideCan:
      "An override can remove libraries from view, lower the rating or quality ceiling, or mark the profile unverified — it can never add access the baseline denied.",
  },
  permission: {
    icon: KeyRound,
    title: "Admin & permissions",
    governs:
      "The acting-admin gate and per-user permissions such as marker editing and metadata curation.",
    example: "“Deny metadata curation on weekends.”",
    baseline: [
      "Disabled accounts are denied everything.",
      "Admin actions require the admin role, used from the household's primary profile (or before any profile is chosen).",
      "Marker editing: admins automatically; everyone else needs the permission assigned to their account.",
      "Metadata curation: same rule, and the item must also live inside the user's allowed libraries.",
    ],
    overrideCan:
      "An override can deny a permission the baseline would grant — it can never grant one the baseline denies.",
  },
  action: {
    icon: MonitorPlay,
    title: "Downloads & playback",
    governs:
      "Download and transcode eligibility, plus how many concurrent streams and transcodes a user may run.",
    example: "“No downloads between 22:00 and 07:00.”",
    baseline: [
      "Downloads require downloads to be enabled on the server and allowed for the user.",
      "Transcoded downloads additionally require transcoding to be enabled, allowed for the user, and a prepared artifact.",
      "New streams are admitted while the user is under their concurrent-stream limit; transcodes also check the transcode limit. A limit of 0 means unlimited.",
    ],
    overrideCan:
      "An override can deny a download or stream the baseline would allow, or tighten the quality ceiling — never the reverse.",
  },
};

const FALLBACK_META: PolicyDomainMeta = {
  icon: ScrollText,
  title: "Policy",
  governs: "Custom decisions for this domain.",
  example: "",
  baseline: [],
  overrideCan: "",
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
