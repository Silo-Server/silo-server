import type { PluginCapability, PluginInstallation, PluginPresentation } from "@/api/types";
import { englishText, t, type MessageKey } from "@/i18n";
import { capabilityEnglishLabel, capabilityKind } from "@/lib/pluginCapabilities";

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

/**
 * A plugin's tier: its repository's source kind, or `unverified` for an
 * installation with no repository (an uploaded file or archive link, or a
 * repository since removed). Those never auto-update (1.0 plugin-management AC5).
 */
export type PluginTier = PluginSourceKind | "unverified";

export function installationTier(installation: PluginInstallation): PluginTier {
  return installation.repository_id == null ? "unverified" : installation.source_kind;
}

/**
 * The newer version an admin can install now. A version recorded before the
 * repository was removed or disabled, or the community catalog was hidden,
 * can't be applied, so it is not offered.
 */
export function pendingUpdateVersion(installation: PluginInstallation): string | null {
  if (installationTier(installation) === "unverified" || installation.updates_paused) return null;
  return installation.available_version || null;
}

/** The plugin's tier, shown on every plugin (1.0 plugin-management AC4). */
export function sourceLabel(tier: string): string {
  return t(sourceLabelKey(tier));
}

function sourceLabelKey(tier: string): MessageKey {
  switch (tier) {
    case "silo":
      return "web.admin.plugins.source.silo";
    case "approved_community":
      return "web.admin.plugins.source.approvedCommunity";
    case "unverified":
      return "web.admin.plugins.source.unverified";
    default:
      return "web.admin.plugins.source.external";
  }
}

/**
 * The notice community, external, and unverified plugins carry on their page.
 * Silo's own plugins have none.
 */
export function tierNotice(tier: string): string | null {
  switch (tier) {
    case "silo":
      return null;
    case "approved_community":
      return t("web.admin.plugins.source.approvedCommunityNotice");
    case "unverified":
      return t("web.admin.plugins.source.unverifiedNotice");
    default:
      return t("web.admin.plugins.source.externalNotice");
  }
}

/** Anchor id of a plugin page's settings panel for one config schema entry. */
export function configPanelId(key: string): string {
  return `config-${key}`;
}

/** The catalog's groups in display order; `titleKey` is formatted where the heading renders. */
export const CATALOG_GROUPS: { kind: PluginSourceKind; titleKey: MessageKey }[] = [
  { kind: "silo", titleKey: "web.admin.plugins.catalog.groupSilo" },
  { kind: "approved_community", titleKey: "web.admin.plugins.catalog.groupApprovedCommunity" },
  { kind: "external", titleKey: "web.admin.plugins.catalog.groupExternal" },
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
    t("web.admin.plugins.tile.noDescription")
  );
}

/**
 * A verified signature's source repository as `owner/name`, from a URL like
 * `https://github.com/owner/name`. Anything else is shown as sent.
 */
export function signatureRepositoryLabel(sourceRepo: string): string {
  try {
    const path = new URL(sourceRepo).pathname.replace(/^\/+|\/+$/g, "");
    return path || sourceRepo;
  } catch {
    return sourceRepo;
  }
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
      label: t("web.admin.plugins.rail.sourceCode"),
      url: safeExternalURL(presentation?.source_url) ?? safeExternalURL(repoURL),
    },
    {
      label: t("web.admin.plugins.rail.changelog"),
      url: safeExternalURL(presentation?.changelog_url),
    },
    {
      label: t("web.admin.plugins.rail.support"),
      url: safeExternalURL(presentation?.support_url),
    },
  ].filter((link): link is { label: string; url: string } => Boolean(link.url));
}

export function licenseLabel(spdx?: string): string {
  const value = spdx?.trim();
  return !value || value === "NOASSERTION"
    ? t("web.admin.plugins.rail.licenseNotSpecified")
    : value;
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
    // Shown labels, and their English so an English term still matches.
    sourceLabel(sourceKind),
    englishText(sourceLabelKey(sourceKind)),
    ...capabilities.flatMap((capability) => [
      capability.display_name,
      capability.description,
      capability.type,
      capabilityKind(capability.type).label,
      capabilityEnglishLabel(capability.type),
    ]),
  ]
    .filter(Boolean)
    .join("\n")
    .toLocaleLowerCase();

  return searchableText.includes(normalizedQuery);
}
