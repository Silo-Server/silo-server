import type { ReactNode } from "react";
import { Link } from "react-router";

import type { WatchProviderStats } from "@/api/types";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminStats } from "@/hooks/queries/admin/stats";
import { formatRelativeTime } from "@/lib/date";
import { SectionError } from "../feedback";
import { formatLocale } from "@/lib/appLocale";
import { useT } from "@/i18n";

/**
 * Connection and 24h sync status for every watch provider.
 *
 * The watch-provider subsystem is pluggable, so this reads the server's
 * per-provider breakdown rather than any one provider's fields: built-in
 * providers and ones a plugin registers at runtime are the same kind of row.
 * One line per provider: it sits in a 1-row slot by default and scrolls once
 * there are more providers than fit.
 */
const SEPARATOR = " · ";
const DIVIDER = "|";

export function WatchProvidersWidget() {
  const t = useT();
  const statsQuery = useAdminStats();
  const providers = statsQuery.data?.watch_providers;

  if (statsQuery.isLoading) {
    return <Skeleton className="h-full min-h-14 rounded-2xl" />;
  }

  if (statsQuery.error) {
    return (
      <div className="surface-panel flex h-full items-center rounded-2xl border-0 px-4">
        <SectionError message={t("web.admin.dashboard.widgets.watchProviders.loadFailed")} />
      </div>
    );
  }

  if (!providers || providers.length === 0) {
    return (
      <div className="surface-panel flex h-full min-h-14 items-center gap-3 rounded-2xl border-0 px-4 py-3">
        <span className="text-muted-foreground text-sm">
          {t("web.admin.dashboard.widgets.watchProviders.empty")}
        </span>
      </div>
    );
  }

  return (
    <div className="surface-panel divide-border/50 flex h-full min-h-14 flex-col divide-y overflow-y-auto rounded-2xl border-0">
      {providers.map((provider) => (
        <ProviderRow key={provider.provider} provider={provider} />
      ))}
    </div>
  );
}

function ProviderRow({ provider }: { provider: WatchProviderStats }) {
  const t = useT();
  const profiles = provider.connected_profiles;
  const errors = provider.sync_errors_24h + provider.failed_exports;

  return (
    <div className="flex min-h-14 flex-shrink-0 flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3">
      <ProviderMark provider={provider} />
      <span className="text-muted-foreground min-w-0 flex-1 text-sm">
        <span className="text-foreground font-semibold">{provider.display_name}</span>
        {profiles === 0 ? (
          <>
            {SEPARATOR}
            {provider.registered
              ? t("web.admin.dashboard.widgets.watchProviders.notConnected")
              : t("web.admin.dashboard.widgets.watchProviders.notInstalled")}
          </>
        ) : (
          <ProviderActivity provider={provider} />
        )}
      </span>
      {errors > 0 ? (
        <span className="bg-destructive/10 text-destructive flex-shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums">
          {t("web.admin.dashboard.widgets.watchProviders.errorCount", {
            count: errors,
            count_text: errors.toLocaleString(formatLocale()),
          })}
        </span>
      ) : null}
      <Link
        to="/admin/tasks/sync_watch_providers"
        className="text-muted-foreground hover:text-primary ml-auto text-[11px] whitespace-nowrap transition-colors"
      >
        {t("web.admin.dashboard.widgets.watchProviders.manage")}
      </Link>
    </div>
  );
}

/**
 * The connected-provider detail line. Counters a provider cannot produce are
 * left out rather than shown as a permanent zero: a list-only provider never
 * exports, so "0 out" would read as a failure instead of an absence.
 */
function ProviderActivity({ provider }: { provider: WatchProviderStats }) {
  const t = useT();
  const profiles = provider.connected_profiles;
  const lastSync =
    formatRelativeTime(provider.last_sync_completed_at, {
      rounding: "floor",
      justNowLabel: t("web.admin.dashboard.widgets.watchProviders.justNow"),
    }) ?? t("web.admin.dashboard.widgets.watchProviders.never");
  const value = (chunks: ReactNode[]) => (
    <span className="text-foreground font-medium">{chunks}</span>
  );

  return (
    <>
      {SEPARATOR}
      {t("web.admin.dashboard.widgets.watchProviders.profilesSynced", {
        count: profiles,
        count_text: profiles.toLocaleString(formatLocale()),
        last_sync: lastSync,
      })}
      <span className="text-border/70 mx-1.5">{DIVIDER}</span>
      {provider.exporting
        ? t("web.admin.dashboard.widgets.watchProviders.importedExported24h", {
            imported: provider.imported_watched_24h.toLocaleString(formatLocale()),
            exported: provider.exported_watched_24h.toLocaleString(formatLocale()),
            value,
          })
        : t("web.admin.dashboard.widgets.watchProviders.imported24h", {
            imported: provider.imported_watched_24h.toLocaleString(formatLocale()),
            value,
          })}
      {provider.scrobbling && provider.open_scrobbles > 0 ? (
        <>
          {SEPARATOR}
          {t("web.admin.dashboard.widgets.watchProviders.scrobbling", {
            count: provider.open_scrobbles,
            count_text: provider.open_scrobbles.toLocaleString(formatLocale()),
            value,
          })}
        </>
      ) : null}
    </>
  );
}

/**
 * A two-letter glyph derived from the provider's own name, so a plugin-supplied
 * provider gets the same treatment as a built-in one without the dashboard
 * shipping a mark for every provider that might ever exist.
 */
function ProviderMark({ provider }: { provider: WatchProviderStats }) {
  const source = provider.display_name || provider.provider;
  const initials = source.slice(0, 2).toUpperCase();

  return (
    <span
      aria-hidden="true"
      className="bg-primary/10 text-primary flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-lg text-[10px] font-extrabold tracking-tight"
    >
      {initials}
    </span>
  );
}
