import { useRef, useState } from "react";
import type { ReactNode, Ref } from "react";
import { AlertTriangle, Check, Download, ShieldAlert, ShieldCheck } from "lucide-react";
import { useLocation } from "react-router";

import type { PluginCatalogEntry, PluginInstallation } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import { useInstallPlugin } from "@/hooks/queries/admin/plugins";
import { useT } from "@/i18n";
import {
  installationTier,
  pendingUpdateVersion,
  pluginDisplayName,
  pluginPagePath,
  pluginSummary,
  signatureRepositoryLabel,
  sourceLabel,
  tierNotice,
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
  linkRef,
}: {
  name: string;
  subline: string;
  to: string;
  dimmed: boolean;
  linkRef?: Ref<HTMLAnchorElement>;
}) {
  const location = useLocation();
  const state: PluginPageLinkState = { from: location.search };
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <PluginMonogram name={name} className={dimmed ? "opacity-70" : undefined} />
      <div className="flex min-w-0 flex-col">
        {/* The name link stretches over the whole tile, so the tile is one target with a short name. */}
        <ViewTransitionLink
          ref={linkRef}
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
  const t = useT();
  const presentation = installation.presentation ?? catalogEntry?.presentation;
  const capabilities = installation.capabilities ?? [];
  const name = pluginDisplayName(installation.plugin_id, presentation);
  const status = pluginStatus(installation);
  const off = !installation.enabled;
  const updateVersion = pendingUpdateVersion(installation);
  const signature = installation.signature;
  // A quiet mark for a verified release; unknown and unsigned show nothing.
  const verifiedLabel =
    signature.state === "verified" && signature.source_repo
      ? t("web.admin.plugins.signature.tileVerified", {
          repository: signatureRepositoryLabel(signature.source_repo),
        })
      : undefined;

  return (
    <TileShell attention={status.attention} dimmed={off} state={off ? "off" : undefined}>
      <TileHeading
        name={name}
        subline={t("web.admin.plugins.tile.installedSubline", {
          version: installation.version,
          source: sourceLabel(installationTier(installation)),
        })}
        to={pluginPagePath(installation.plugin_id)}
        dimmed={off}
      />
      <TileSummary>{pluginSummary(presentation, capabilities)}</TileSummary>
      <TileFooter>
        <span className="inline-flex min-w-0 items-center gap-1.5">
          <PluginStatusLabel {...status} />
          {verifiedLabel ? (
            <span
              role="img"
              aria-label={verifiedLabel}
              title={verifiedLabel}
              className="text-muted-foreground relative z-10 inline-flex"
            >
              <ShieldCheck aria-hidden="true" className="size-3.5 shrink-0" />
            </span>
          ) : null}
        </span>
        {signature.state === "invalid" ? (
          <span className="text-warning inline-flex min-w-0 items-center gap-1 text-[12.5px]">
            <ShieldAlert aria-hidden="true" className="size-3.5 shrink-0" />
            <span className="truncate">{t("web.admin.plugins.signature.tileInvalid")}</span>
          </span>
        ) : installation.update_failure ? (
          <span
            className="text-warning inline-flex min-w-0 items-center gap-1 text-[12.5px]"
            title={installation.update_failure.reason}
          >
            <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0" />
            <span className="truncate">
              {t("web.admin.plugins.tile.updateFailed", {
                version: installation.update_failure.version,
              })}
            </span>
          </span>
        ) : updateVersion ? (
          <span className="bg-surface-raised text-foreground/85 inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[12.5px]">
            <Download aria-hidden="true" className="size-3.5" />
            {t("web.admin.plugins.tile.updateAvailable", { version: updateVersion })}
          </span>
        ) : installation.updates_paused ? (
          <span className="text-muted-foreground text-[12.5px]">
            {t("web.admin.plugins.tile.updatesPaused")}
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
  const t = useT();
  const installPlugin = useInstallPlugin();
  const capabilities = entry.capabilities ?? [];
  const name = pluginDisplayName(entry.plugin_id, entry.presentation);
  const publisher = entry.presentation?.publisher_name?.trim();
  // Community and external plugins carry a tier notice; the admin confirms it
  // before installing, since the tile skips the plugin page that shows it.
  const notice = tierNotice(entry.source_kind);
  const [confirmInstall, setConfirmInstall] = useState(false);
  // The dialog has no Radix trigger, so focus returns to the Install button by
  // hand; otherwise it falls to the page body and a keyboard user loses their
  // place in the catalog. After Confirm the button is disabled while the
  // install runs, then replaced by "Installed", so focus goes to the tile's
  // name link instead.
  const installButton = useRef<HTMLButtonElement>(null);
  const nameLink = useRef<HTMLAnchorElement>(null);

  function install() {
    installPlugin.mutate({
      repository_id: entry.repository_id,
      plugin_id: entry.plugin_id,
      version: entry.version,
    });
  }

  return (
    <TileShell dimmed={isInstalled} state={isInstalled ? "installed" : undefined}>
      <TileHeading
        name={name}
        // The catalog group heading already names the tier.
        subline={
          publisher
            ? t("web.admin.plugins.tile.catalogSubline", { publisher, version: entry.version })
            : entry.version
        }
        to={pluginPagePath(entry.plugin_id, {
          repositoryId: entry.repository_id,
          version: entry.version,
        })}
        dimmed={isInstalled}
        linkRef={nameLink}
      />
      <TileSummary>{pluginSummary(entry.presentation, capabilities)}</TileSummary>
      <TileFooter>
        <PluginCapabilityIcons capabilities={capabilities} />
        {isInstalled ? (
          <span className="text-muted-foreground relative z-10 inline-flex items-center gap-1 text-[12.5px]">
            <Check aria-hidden="true" className="size-3.5" />
            {t("web.admin.plugins.tile.installed")}
          </span>
        ) : (
          <Button
            size="xs"
            variant="outline"
            className="relative z-10"
            ref={installButton}
            aria-label={t("web.admin.plugins.tile.installLabel", { name })}
            disabled={installPlugin.isPending}
            onClick={() => (notice ? setConfirmInstall(true) : install())}
          >
            <Download aria-hidden="true" />
            {installPlugin.isPending
              ? t("web.admin.plugins.tile.installing")
              : t("web.admin.plugins.tile.install")}
          </Button>
        )}
      </TileFooter>
      {notice ? (
        <ConfirmDialog
          open={confirmInstall}
          onOpenChange={setConfirmInstall}
          title={t("web.admin.plugins.tile.confirmTitle", { name })}
          description={t("web.admin.plugins.tile.confirmDescription", {
            source: sourceLabel(entry.source_kind),
            notice,
          })}
          confirmLabel={t("web.admin.plugins.tile.confirmInstall")}
          onConfirm={install}
          isPending={installPlugin.isPending}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            const button = installButton.current;
            (button && !button.disabled ? button : nameLink.current)?.focus();
          }}
        />
      ) : null}
    </TileShell>
  );
}
