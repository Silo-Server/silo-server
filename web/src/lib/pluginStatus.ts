import type { PluginInstallation, PluginPresentation } from "@/api/types";
import { missingRequiredConfig } from "@/lib/pluginConfigReady";
import { pluginDisplayName } from "@/lib/pluginPresentation";
import { pluginStatusIndicator } from "@/lib/pluginStatusIndicator";

export interface PluginStatus {
  label: string;
  dotClass: string;
  /** Text color for the label; empty for the neutral states. */
  textClass: string;
  title?: string;
  /** The plugin needs an admin: it is failing, restarting, or missing required settings. */
  attention: boolean;
  /** Sort rank: problems first (most severe first), healthy plugins next, off plugins last. */
  rank: number;
}

/**
 * The plugins page's status for an installation. It adds "Needs setup" to
 * pluginStatusIndicator's runtime states. A plugin that is off never needs
 * attention; the Off filter covers it.
 */
export function pluginStatus(installation: PluginInstallation): PluginStatus {
  const indicator = pluginStatusIndicator(installation);
  if (!installation.enabled) {
    return { ...indicator, textClass: "text-muted-foreground", attention: false, rank: 4 };
  }
  if (installation.runtime.resident && installation.runtime.state === "failed") {
    return { ...indicator, textClass: "text-destructive", attention: true, rank: 0 };
  }
  if (installation.runtime.resident && installation.runtime.state === "backoff") {
    return { ...indicator, textClass: "text-warning", attention: true, rank: 1 };
  }
  const missing = missingRequiredConfig(installation);
  if (missing.length > 0) {
    return {
      label: "Needs setup",
      dotClass: "bg-warning",
      textClass: "text-warning",
      title: `Missing: ${missing.map((entry) => entry.title || entry.key).join(", ")}`,
      attention: true,
      rank: 2,
    };
  }
  return { ...indicator, textClass: "", attention: false, rank: 3 };
}

export type InstalledFilter = "all" | "attention" | "update" | "off";

export function parseInstalledFilter(raw: string | null): InstalledFilter {
  return raw === "attention" || raw === "update" || raw === "off" ? raw : "all";
}

export function matchesInstalledFilter(
  installation: PluginInstallation,
  filter: InstalledFilter,
): boolean {
  switch (filter) {
    case "attention":
      return pluginStatus(installation).attention;
    case "update":
      return Boolean(installation.available_version);
    case "off":
      return !installation.enabled;
    default:
      return true;
  }
}

/**
 * Sorts by pluginStatus rank, then by display name. `presentationFor` lets the
 * caller supply the same catalog fallback the tiles use for older manifests.
 */
export function sortInstalledPlugins(
  installations: PluginInstallation[],
  presentationFor: (installation: PluginInstallation) => PluginPresentation | undefined = (
    installation,
  ) => installation.presentation,
): PluginInstallation[] {
  const keyed = installations.map((installation) => ({
    installation,
    rank: pluginStatus(installation).rank,
    name: pluginDisplayName(installation.plugin_id, presentationFor(installation)),
  }));
  keyed.sort(
    (a, b) => a.rank - b.rank || a.name.localeCompare(b.name, undefined, { sensitivity: "base" }),
  );
  return keyed.map((entry) => entry.installation);
}
