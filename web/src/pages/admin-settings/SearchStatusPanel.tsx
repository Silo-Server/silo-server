import { Link } from "react-router";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCatalogSearchStatus } from "@/hooks/queries/admin/settings";
import { useT, type Translate } from "@/i18n";
import { formatDateTime } from "@/lib/datetime";

import { SettingFieldStatus } from "./SettingField";
import { formatLocale } from "@/lib/appLocale";
import { formatDecimal } from "@/lib/numberFormat";

/**
 * Read-only view of the catalog search index: which provider is answering
 * queries, how much of the catalog is indexed, and links to the rebuild/sync
 * tasks. Diagnostics, not settings — it lives behind a disclosure.
 */
const MEILISEARCH = "Meilisearch";

export function SearchStatusPanel() {
  const t = useT();
  const { data: status, isLoading, isError, error } = useCatalogSearchStatus();

  if (isLoading) {
    return (
      <div className="space-y-3 py-3">
        <Skeleton className="h-5 w-56" />
        <Skeleton className="h-5 w-72" />
        <Skeleton className="h-5 w-48" />
      </div>
    );
  }

  // A failed status request used to leave the skeletons up forever, which reads
  // as "still loading" — the one thing a diagnostics panel must never imply
  // about a request that already came back. Say so, and keep the rebuild/sync
  // links reachable: they are exactly what an admin wants when the index is in
  // a bad enough state that the status endpoint is failing.
  if (isError || !status) {
    return (
      <div className="space-y-3 py-3">
        <SettingFieldStatus tone="warn">
          {error instanceof Error && error.message
            ? t("web.admin.settings.search.status.loadErrorWithReason", { reason: error.message })
            : t("web.admin.settings.search.status.loadError")}
        </SettingFieldStatus>
        <SearchTaskLinks />
      </div>
    );
  }

  return (
    <div className="divide-border divide-y">
      {status.degraded && (
        <div className="py-3">
          <SettingFieldStatus tone="warn">
            {status.degraded_reason ?? t("web.admin.settings.search.degradedFallback")}
          </SettingFieldStatus>
        </div>
      )}
      <StatusRow
        label={t("web.admin.settings.search.status.answering")}
        value={
          status.active_provider === "meilisearch"
            ? status.index.rebuild_required
              ? t("web.admin.settings.search.status.meiliKeywordOnly")
              : MEILISEARCH
            : t("web.admin.settings.search.status.postgresFullText")
        }
        badge={status.configured_provider}
      />
      <StatusRow
        label={t("web.admin.settings.search.status.health")}
        value={
          status.meilisearch.healthy
            ? t("web.admin.settings.search.status.healthy")
            : status.meilisearch.circuit_state
        }
        badge={
          status.meilisearch.configured
            ? t("web.admin.settings.search.status.configured")
            : t("web.admin.settings.search.status.notConfigured")
        }
      />
      <StatusRow
        label={t("web.admin.settings.search.status.activeIndex")}
        value={status.index.active_index_uid || t("web.admin.settings.search.status.notBuilt")}
        badge={
          status.index.rebuild_required
            ? t("web.admin.settings.search.status.rebuildRequired")
            : t("web.admin.settings.search.status.schema", {
                version: status.index.schema_version,
                expected: status.index.expected_schema_version,
              })
        }
      />
      <StatusRow
        label={t("web.admin.settings.search.status.documents")}
        value={String(status.index.document_count)}
      />
      <StatusRow
        label={t("web.admin.settings.search.status.indexedTypes")}
        value={formatIndexedTypes(t, status.meilisearch.index_types)}
      />
      <StatusRow
        label={t("web.admin.settings.search.status.compressedVectors")}
        value={
          status.meilisearch.binary_quantized
            ? t("web.admin.settings.search.status.enabled")
            : t("web.admin.settings.search.status.disabled")
        }
      />
      <StatusRow
        label={t("web.admin.settings.search.status.semantic")}
        value={
          status.meilisearch.semantic_enabled
            ? t("web.admin.settings.search.status.enabled")
            : t("web.admin.settings.search.status.disabled")
        }
        badge={status.meilisearch.embedder}
      />
      <StatusRow
        label={t("web.admin.settings.search.status.semanticRatio")}
        value={formatSemanticRatio(status.meilisearch.semantic_ratio)}
      />
      <StatusRow
        label={t("web.admin.settings.search.status.itemsWithVectors")}
        value={String(status.index.vector_document_count)}
      />
      {status.semantic && (
        <>
          <StatusRow
            label={t("web.admin.settings.search.status.semanticReadiness")}
            value={
              status.semantic.ready
                ? t("web.admin.settings.search.status.ready")
                : t("web.admin.settings.search.status.notReady")
            }
            badge={status.semantic.ready ? undefined : status.semantic.disabled_reason}
          />
          <StatusRow
            label={t("web.admin.settings.search.status.vectorCoverage")}
            value={formatPercent(status.semantic.vector_coverage_ratio)}
          />
          <StatusRow
            label={t("web.admin.settings.search.status.coverageUpdated")}
            value={
              formatStatusDate(status.semantic.coverage_updated_at) ||
              t("web.admin.settings.search.status.never")
            }
          />
          <StatusRow
            label={t("web.admin.settings.search.status.embeddingModel")}
            value={
              status.semantic.capability.ok
                ? t("web.admin.settings.search.status.ok")
                : (status.semantic.capability.reason ??
                  t("web.admin.settings.search.status.unavailable"))
            }
            badge={status.semantic.capability.embedder}
          />
          {status.semantic.per_type && status.semantic.per_type.length > 0 && (
            <div className="flex flex-col gap-2 py-3">
              <span className="text-sm font-medium">
                {t("web.admin.settings.search.status.coverageByType")}
              </span>
              <div className="divide-border/60 divide-y">
                {status.semantic.per_type.map((entry) => (
                  <div key={entry.type} className="flex items-center justify-between gap-2 py-1.5">
                    <span className="text-muted-foreground min-w-0 truncate text-sm">
                      {t("web.admin.settings.search.status.typeCoverage", {
                        type: entry.type,
                        vectorized: entry.vectorized,
                        eligible: entry.eligible,
                        percent: formatPercent(entry.vector_coverage_ratio),
                      })}
                    </span>
                    <Badge variant={entry.ready ? "secondary" : "outline"}>
                      {entry.ready
                        ? t("web.admin.settings.search.status.ready")
                        : t("web.admin.settings.search.status.notReady")}
                    </Badge>
                  </div>
                ))}
              </div>
            </div>
          )}
        </>
      )}
      <StatusRow
        label={t("web.admin.settings.search.status.pendingEvents")}
        value={String(status.index.pending_events)}
      />
      {status.index.dead_lettered_events > 0 && (
        <StatusRow
          label={t("web.admin.settings.search.status.droppedEvents")}
          value={String(status.index.dead_lettered_events)}
          badge={t("web.admin.settings.search.status.staleUntilRebuild")}
        />
      )}
      <StatusRow
        label={t("web.admin.settings.search.status.lastSync")}
        value={
          formatStatusDate(status.index.last_sync_at) || t("web.admin.settings.search.status.never")
        }
      />
      {status.meilisearch.last_fallback && (
        <StatusRow
          label={t("web.admin.settings.search.status.lastFallback")}
          value={status.meilisearch.last_fallback}
        />
      )}
      <SearchTaskLinks />
    </div>
  );
}

/**
 * One line of the index status for the Search engine row, so an admin sees
 * whether Meilisearch is keeping up without opening Advanced. Shares the
 * status query with the full panel.
 */
export function SearchStatusSummary() {
  const t = useT();
  const { data: status } = useCatalogSearchStatus();
  if (!status) return null;
  const lastSync =
    formatStatusDate(status.index.last_sync_at) ||
    t("web.admin.settings.search.status.summaryNever");
  return (
    <SettingFieldStatus tone="muted">
      {t("web.admin.settings.search.status.summary", {
        last_sync: lastSync,
        count: status.index.document_count,
        count_text: status.index.document_count.toLocaleString(formatLocale()),
      })}
    </SettingFieldStatus>
  );
}

/** The two index maintenance tasks. Shown whether or not the status resolved. */
function SearchTaskLinks() {
  const t = useT();
  return (
    <div className="flex flex-wrap gap-2 py-3">
      <Button asChild size="sm" variant="outline">
        <Link to="/admin/tasks/rebuild_catalog_search_index">
          {t("web.admin.settings.search.status.rebuildIndex")}
        </Link>
      </Button>
      <Button asChild size="sm" variant="ghost">
        <Link to="/admin/tasks/sync_catalog_search_index">
          {t("web.admin.settings.search.status.automaticMaintenance")}
        </Link>
      </Button>
    </div>
  );
}

function StatusRow({ label, value, badge }: { label: string; value: string; badge?: string }) {
  return (
    <div className="flex flex-col gap-2 py-3 sm:flex-row sm:items-center sm:justify-between">
      <span className="text-sm font-medium">{label}</span>
      <span className="text-muted-foreground flex min-w-0 flex-wrap items-center gap-2 text-sm">
        <span className="max-w-full text-end break-words">{value}</span>
        {badge ? <Badge variant="outline">{badge}</Badge> : null}
      </span>
    </div>
  );
}

function formatStatusDate(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return formatDateTime(date);
}

function formatIndexedTypes(t: Translate, value?: string[]) {
  if (!value || value.length === 0) return t("web.admin.settings.search.status.allTypes");
  return value.join(", ");
}

function formatSemanticRatio(value?: number) {
  if (typeof value !== "number" || Number.isNaN(value)) return "0";
  return formatDecimal(value, 2);
}

function formatPercent(value?: number) {
  if (typeof value !== "number" || Number.isNaN(value)) return "0%";
  return `${Math.round(value * 100)}%`;
}
