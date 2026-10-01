import type { ReactNode } from "react";
import { Check, Download } from "lucide-react";
import { useLocation } from "react-router";

import type { PluginCatalogEntry, PluginInstallation } from "@/api/types";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import { useInstallPlugin } from "@/hooks/queries/admin/plugins";
import {
  pluginDisplayName,
  pluginPagePath,
  pluginSummary,
  sourceLabel,
} from "@/lib/pluginPresentation";
import { pluginStatus } from "@/lib/pluginStatus";
import { cn } from "@/lib/utils";

import { PluginCapabilityIcons, PluginMonogram, PluginStatusLabel } from "./PluginVisuals";

/** Router state a tile hands to the plugin page so its back link restores the list's filters. */
export interface PluginPageLinkState {
  from?: string;
}

export function PluginTileGrid({ children }: { children: ReactNode }) {
  return (
    <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-[repeat(auto-fill,minmax(232px,1fr))]">
      {children}
    </div>
  );
}

function TileShell({
  attention = false,
  dimmed = false,
  state,
  children,
}: {
  attention?: boolean;
  dimmed?: boolean;
  state?: string;
  children: ReactNode;
}) {
  return (
    <article
      data-attention={attention ? "true" : undefined}
      data-state={state}
      className={cn(
        "relative flex min-w-0 flex-col gap-2.5 rounded-xl border px-3.5 pt-3.5 transition-colors",
        dimmed ? "bg-transparent" : "bg-card hover:bg-surface-hover",
        attention ? "border-warning/40" : "border-border",
      )}
    >
      {children}
    </article>
  );
}

function TileHeading({
  name,
  subline,
  to,
  dimmed,
}: {
  name: string;
  subline: string;
  to: string;
  dimmed: boolean;
}) {
  const location = useLocation();
  const state: PluginPageLinkState = { from: location.search };
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <PluginMonogram name={name} className={dimmed ? "opacity-70" : undefined} />
      <div className="flex min-w-0 flex-col">
        {/* The name link stretches over the whole tile, so the tile is one target with a short name. */}
        <ViewTransitionLink
          to={to}
          state={state}
          className={cn(
            "truncate text-sm leading-tight font-semibold outline-none",
            "after:absolute after:inset-0 after:rounded-xl",
            "focus-visible:after:ring-ring focus-visible:after:ring-2",
            dimmed && "opacity-70",
          )}
        >
          {name}
        </ViewTransitionLink>
        <span className="text-muted-foreground truncate text-[12.5px]">{subline}</span>
      </div>
    </div>
  );
}

function TileSummary({ children }: { children: ReactNode }) {
  return (
    <p className="text-muted-foreground line-clamp-2 min-h-9 text-[13px] leading-snug">
      {children}
    </p>
  );
}

function TileFooter({ children }: { children: ReactNode }) {
  return (
    <div className="mt-auto flex min-h-10 items-center justify-between gap-2 border-t text-[13px]">
      {children}
    </div>
  );
}

export function InstalledPluginTile({
  installation,
  catalogEntry,
}: {
  installation: PluginInstallation;
  catalogEntry?: PluginCatalogEntry;
}) {
  const presentation = installation.presentation ?? catalogEntry?.presentation;
  const capabilities = installation.capabilities ?? [];
  const name = pluginDisplayName(installation.plugin_id, presentation);
  const status = pluginStatus(installation);
  const off = !installation.enabled;

  return (
    <TileShell attention={status.attention} dimmed={off} state={off ? "off" : undefined}>
      <TileHeading
        name={name}
        subline={`${installation.version}, ${sourceLabel(installation.source_kind)}`}
        to={pluginPagePath(installation.plugin_id)}
        dimmed={off}
      />
      <TileSummary>{pluginSummary(presentation, capabilities)}</TileSummary>
      <TileFooter>
        <PluginStatusLabel {...status} />
        {installation.available_version ? (
          <span className="bg-surface-raised text-foreground/85 inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[12.5px]">
            <Download aria-hidden="true" className="size-3.5" />
            {installation.available_version} available
          </span>
        ) : (
          <PluginCapabilityIcons capabilities={capabilities} />
        )}
      </TileFooter>
    </TileShell>
  );
}

export function CatalogPluginTile({
  entry,
  isInstalled,
}: {
  entry: PluginCatalogEntry;
  isInstalled: boolean;
}) {
  const installPlugin = useInstallPlugin();
  const capabilities = entry.capabilities ?? [];
  const name = pluginDisplayName(entry.plugin_id, entry.presentation);
  const publisher = entry.presentation?.publisher_name?.trim();

  return (
    <TileShell dimmed={isInstalled} state={isInstalled ? "installed" : undefined}>
      <TileHeading
        name={name}
        // The catalog group heading already names the tier.
        subline={`${publisher ? `by ${publisher}, ` : ""}${entry.version}`}
        to={pluginPagePath(entry.plugin_id, {
          repositoryId: entry.repository_id,
          version: entry.version,
        })}
        dimmed={isInstalled}
      />
      <TileSummary>{pluginSummary(entry.presentation, capabilities)}</TileSummary>
      <TileFooter>
        <PluginCapabilityIcons capabilities={capabilities} />
        {isInstalled ? (
          <span className="text-muted-foreground relative z-10 inline-flex items-center gap-1 text-[12.5px]">
            <Check aria-hidden="true" className="size-3.5" />
            Installed
          </span>
        ) : (
          <Button
            size="xs"
            variant="outline"
            className="relative z-10"
            aria-label={`Install ${name}`}
            disabled={installPlugin.isPending}
            onClick={() =>
              installPlugin.mutate({
                repository_id: entry.repository_id,
                plugin_id: entry.plugin_id,
                version: entry.version,
              })
            }
          >
            <Download aria-hidden="true" />
            {installPlugin.isPending ? "Installing…" : "Install"}
          </Button>
        )}
      </TileFooter>
    </TileShell>
  );
}
