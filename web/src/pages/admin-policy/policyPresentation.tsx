import { Eye, KeyRound, MonitorPlay, ScrollText, type LucideIcon } from "lucide-react";

import type { PolicyDocument } from "@/api/adminPolicy";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { cn } from "@/lib/utils";

import { formatPolicyDomain } from "./policyPageUtils";

export interface PolicyDomainMeta {
  icon: LucideIcon;
  title: string;
  /** Plain-language summary of what this domain governs. */
  governs: string;
  /** A concrete example of a narrowing rule an admin might write, unquoted. */
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

interface DomainMetaKeys {
  icon: LucideIcon;
  title: MessageKey;
  governs: MessageKey;
  example: MessageKey;
  baseline: MessageKey[];
  overrideCan: MessageKey;
}

const DOMAIN_META: Record<string, DomainMetaKeys> = {
  scope: {
    icon: Eye,
    title: "web.admin.access.policy.domain.scope.title",
    governs: "web.admin.access.policy.domain.scope.governs",
    example: "web.admin.access.policy.domain.scope.example",
    baseline: [
      "web.admin.access.policy.domain.scope.baseline1",
      "web.admin.access.policy.domain.scope.baseline2",
      "web.admin.access.policy.domain.scope.baseline3",
      "web.admin.access.policy.domain.scope.baseline4",
      "web.admin.access.policy.domain.scope.baseline5",
    ],
    overrideCan: "web.admin.access.policy.domain.scope.overrideCan",
  },
  permission: {
    icon: KeyRound,
    title: "web.admin.access.policy.domain.permission.title",
    governs: "web.admin.access.policy.domain.permission.governs",
    example: "web.admin.access.policy.domain.permission.example",
    baseline: [
      "web.admin.access.policy.domain.permission.baseline1",
      "web.admin.access.policy.domain.permission.baseline2",
      "web.admin.access.policy.domain.permission.baseline3",
      "web.admin.access.policy.domain.permission.baseline4",
    ],
    overrideCan: "web.admin.access.policy.domain.permission.overrideCan",
  },
  action: {
    icon: MonitorPlay,
    title: "web.admin.access.policy.domain.action.title",
    governs: "web.admin.access.policy.domain.action.governs",
    example: "web.admin.access.policy.domain.action.example",
    baseline: [
      "web.admin.access.policy.domain.action.baseline1",
      "web.admin.access.policy.domain.action.baseline2",
      "web.admin.access.policy.domain.action.baseline3",
    ],
    overrideCan: "web.admin.access.policy.domain.action.overrideCan",
  },
};

/** A domain's presentation in the current language (`t` from the calling component). */
export function policyDomainMeta(domain: string, t: Translate): PolicyDomainMeta {
  const keys = Object.hasOwn(DOMAIN_META, domain) ? DOMAIN_META[domain] : undefined;
  if (!keys) {
    return {
      icon: ScrollText,
      title: formatPolicyDomain(domain),
      governs: t("web.admin.access.policy.domain.unknownGoverns"),
      example: "",
      baseline: [],
      overrideCan: "",
    };
  }
  return {
    icon: keys.icon,
    title: t(keys.title),
    governs: t(keys.governs),
    example: t(keys.example),
    baseline: keys.baseline.map((key) => t(key)),
    overrideCan: t(keys.overrideCan),
  };
}

export type PolicyDocumentStatus = "live" | "draft" | "disabled";

export function policyDocumentStatus(document: PolicyDocument): PolicyDocumentStatus {
  if (!document.enabled) return "disabled";
  return document.active_version_id ? "live" : "draft";
}

const STATUS_PRESENTATION: Record<PolicyDocumentStatus, { label: MessageKey; dot: string }> = {
  live: { label: "web.admin.access.policy.status.active", dot: "bg-emerald-400" },
  draft: { label: "web.admin.access.policy.status.draft", dot: "bg-amber-400" },
  disabled: { label: "web.admin.access.policy.status.disabled", dot: "bg-muted-foreground/50" },
};

interface PolicyStatusPillProps {
  status: PolicyDocumentStatus;
  /** Version number to show next to a live status, when known. */
  versionNumber?: number;
  className?: string;
}

export function PolicyStatusPill({ status, versionNumber, className }: PolicyStatusPillProps) {
  const t = useT();
  const presentation = STATUS_PRESENTATION[status];
  return (
    <span
      className={cn(
        "border-border text-foreground/80 inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium",
        className,
      )}
    >
      <span aria-hidden className={cn("size-1.5 rounded-full", presentation.dot)} />
      {t(presentation.label)}
      {status === "live" && versionNumber !== undefined && (
        <span className="text-muted-foreground">
          {t("web.admin.access.policy.versionNumber", { version: String(versionNumber) })}
        </span>
      )}
    </span>
  );
}
