import { Link } from "react-router";

import type { WatchProviderStats } from "@/api/types";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminStats } from "@/hooks/queries/admin/stats";
import { formatRelativeTime } from "@/lib/date";
import { SectionError } from "../feedback";

/**
 * Connection and 24h sync status for every watch provider.
 *
 * The watch-provider subsystem is pluggable, so this reads the server's
 * per-provider breakdown rather than any one provider's fields: built-in
 * providers and ones a plugin registers at runtime are the same kind of row.
 * The widget keeps the one-line-per-provider strip shape it had as a
 * Trakt-only tile — it sits in a 1-row slot by default and scrolls once there
 * are more providers than fit.
 */
export function WatchProvidersWidget() {
  const statsQuery = useAdminStats();
  const providers = statsQuery.data?.watch_providers;

  if (statsQuery.isLoading) {
    return <Skeleton className="h-full min-h-14 rounded-2xl" />;
  }

  if (statsQuery.error) {
    return (
      <div className="surface-panel flex h-full items-center rounded-2xl border-0 px-4">
        <SectionError message="Failed to load watch provider activity." />
      </div>
    );
  }

  if (!providers || providers.length === 0) {
    return (
      <div className="surface-panel flex h-full min-h-14 items-center gap-3 rounded-2xl border-0 px-4 py-3">
        <span className="text-muted-foreground text-sm">No watch providers configured</span>
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
  const profiles = provider.connected_profiles;
  const errors = provider.sync_errors_24h + provider.failed_exports;

  return (
    <div className="flex min-h-14 flex-shrink-0 flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3">
      <ProviderMark provider={provider} />
      <span className="text-muted-foreground min-w-0 flex-1 text-sm">
        <span className="text-foreground font-semibold">{provider.display_name}</span>
        {profiles === 0 ? (
          <>
            {" · "}
            {provider.registered ? "not connected" : "not installed"}
          </>
        ) : (
          <ProviderActivity provider={provider} />
        )}
      </span>
      {errors > 0 ? (
        <span className="bg-destructive/10 text-destructive flex-shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums">
          {errors.toLocaleString()} {errors === 1 ? "error" : "errors"}
        </span>
      ) : null}
      <Link
        to="/admin/tasks/sync_watch_providers"
        className="text-muted-foreground hover:text-primary ml-auto text-[11px] whitespace-nowrap transition-colors"
      >
        Manage ›
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
  const profiles = provider.connected_profiles;
  const lastSync =
    formatRelativeTime(provider.last_sync_completed_at, {
      rounding: "floor",
      justNowLabel: "Just now",
    }) ?? "never";

  return (
    <>
      {" · "}
      {profiles.toLocaleString()} {profiles === 1 ? "profile" : "profiles"}
      {" · synced "}
      {lastSync}
      <span className="text-border/70 mx-1.5">|</span>
      {"24h: "}
      <span className="text-foreground font-medium">
        {provider.imported_watched_24h.toLocaleString()}
      </span>
      {" in"}
      {provider.exporting ? (
        <>
          {" / "}
          <span className="text-foreground font-medium">
            {provider.exported_watched_24h.toLocaleString()}
          </span>
          {" out"}
        </>
      ) : null}
      {provider.scrobbling && provider.open_scrobbles > 0 ? (
        <>
          {" · "}
          <span className="text-foreground font-medium">
            {provider.open_scrobbles.toLocaleString()}
          </span>
          {" scrobbling"}
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
