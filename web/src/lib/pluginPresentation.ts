import type { PluginCapability, PluginInstallation, PluginPresentation } from "@/api/types";
import { capabilityKind } from "@/lib/pluginCapabilities";

export type PluginSourceKind = PluginInstallation["source_kind"];

/** Admin page for one plugin. Every plugin link goes through here: the SDK allows any non-empty ID. */
export function pluginPagePath(
  pluginID: string,
  catalogSelection?: { repositoryId: number; version: string },
): string {
  const path = `/admin/plugins/${encodeURIComponent(pluginID)}`;
  if (!catalogSelection) return path;
  const query = new URLSearchParams({
    repository: String(catalogSelection.repositoryId),
    version: catalogSelection.version,
  });
  return `${path}?${query}`;
}

/** The plugin's tier, shown on every plugin (1.0 plugin-management AC4). */
export function sourceLabel(sourceKind: string): string {
  switch (sourceKind) {
    case "silo":
      return "Silo maintained";
    case "approved_community":
      return "Approved community";
    default:
      return "External source";
  }
}

/**
 * The notice community and external plugins carry on their page. Silo's own
 * plugins have none.
 */
export function tierNotice(sourceKind: string): string | null {
  switch (sourceKind) {
    case "silo":
      return null;
    case "approved_community":
      return "Reviewed by Silo maintainers to work as described and be safe for its documented use. Its author maintains and supports it, not the Silo project.";
    default:
      return "Silo has not reviewed this plugin. It comes from a repository or file you added, so only run it if you trust its source.";
  }
}

/** Anchor id of a plugin page's settings panel for one config schema entry. */
export function configPanelId(key: string): string {
  return `config-${key}`;
}

export const CATALOG_GROUPS: { kind: PluginSourceKind; title: string }[] = [
  { kind: "silo", title: "Made by Silo" },
  { kind: "approved_community", title: "Approved community" },
  { kind: "external", title: "Other sources" },
];

export function pluginDisplayName(pluginID: string, presentation?: PluginPresentation): string {
  const displayName = presentation?.display_name.trim();
  if (displayName) return displayName;

  const derived = pluginID
    .replace(/^silo[._-]?/, "")
    .split(/[._-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
  // An ID like `silo` normalizes to nothing; show the raw ID rather than a blank name.
  return derived || pluginID;
}

export function pluginSummary(
  presentation: PluginPresentation | undefined,
  capabilities: PluginCapability[],
): string {
  const summary = presentation?.summary.trim();
  if (summary) return summary;

  return (
    capabilities.find((capability) => capability.description?.trim())?.description?.trim() ??
    "No description provided."
  );
}

export function safeExternalURL(rawURL?: string): string | undefined {
  if (!rawURL) return undefined;
  try {
    const url = new URL(rawURL);
    return url.protocol === "http:" || url.protocol === "https:" ? url.toString() : undefined;
  } catch {
    return undefined;
  }
}

export function pluginResourceLinks(
  presentation: PluginPresentation | undefined,
  repoURL?: string,
): { label: string; url: string }[] {
  return [
    {
      label: "Source code",
      url: safeExternalURL(presentation?.source_url) ?? safeExternalURL(repoURL),
    },
    { label: "Changelog", url: safeExternalURL(presentation?.changelog_url) },
    { label: "Support", url: safeExternalURL(presentation?.support_url) },
  ].filter((link): link is { label: string; url: string } => Boolean(link.url));
}

export function licenseLabel(spdx?: string): string {
  const value = spdx?.trim();
  return !value || value === "NOASSERTION" ? "Not specified" : value;
}

export function pluginMatchesSearch({
  query,
  pluginID,
  presentation,
  capabilities,
  sourceKind,
  repositoryName,
}: {
  query: string;
  pluginID: string;
  presentation?: PluginPresentation;
  capabilities: PluginCapability[];
  sourceKind: string;
  repositoryName?: string;
}): boolean {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  if (!normalizedQuery) return true;

  const searchableText = [
    pluginID,
    pluginDisplayName(pluginID, presentation),
    presentation?.summary,
    presentation?.description_markdown,
    presentation?.publisher_name,
    repositoryName,
    sourceLabel(sourceKind),
    ...capabilities.flatMap((capability) => [
      capability.display_name,
      capability.description,
      capability.type,
      capabilityKind(capability.type).label,
    ]),
  ]
    .filter(Boolean)
    .join("\n")
    .toLocaleLowerCase();

  return searchableText.includes(normalizedQuery);
}
