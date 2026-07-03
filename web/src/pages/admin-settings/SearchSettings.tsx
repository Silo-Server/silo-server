import { useMemo, useState } from "react";
import { Link } from "react-router";
import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCatalogSearchStatus,
  useCheckAdminSettingsConnection,
} from "@/hooks/queries/admin/settings";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";

const MEILI_KEYS = [
  "catalog.search.meilisearch.url",
  "catalog.search.meilisearch.api_key",
  "catalog.search.meilisearch.index",
  "catalog.search.meilisearch.timeout_ms",
  "catalog.search.meilisearch.matching_strategy",
  "catalog.search.meilisearch.sync_batch_size",
  "catalog.search.meilisearch.rebuild_batch_size",
  "catalog.search.meilisearch.rebuild_task_queue_depth",
  "catalog.search.meilisearch.index_types",
  "catalog.search.meilisearch.semantic_enabled",
  "catalog.search.meilisearch.semantic_ratio",
  "catalog.search.meilisearch.embedder",
];

const KEYS = ["catalog.search.provider", ...MEILI_KEYS];

export default function SearchSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const { data: status, isLoading: statusLoading } = useCatalogSearchStatus();
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);
  const provider = form.getValue("catalog.search.provider") || "postgres";
  const meiliEnabled = provider === "meilisearch";

  async function handleCheckConnection() {
    try {
      setConnectionResult(
        await checkConnection.mutateAsync({
          kind: "meilisearch",
          body: form.buildConnectionCheckRequest(MEILI_KEYS),
        }),
      );
    } catch (error) {
      setConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  if (form.isLoading) {
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-40" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-40 w-full" />
        <span className="sr-only">Loading settings</span>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Search</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure catalog search provider selection, Meilisearch connectivity, and index status.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Provider">
          <SettingField
            label="Preferred Provider"
            type="select"
            value={provider}
            onChange={(value) => form.setValue("catalog.search.provider", value)}
            options={[
              { value: "postgres", label: "Postgres FTS" },
              { value: "meilisearch", label: "Meilisearch" },
            ]}
          />
        </FieldGroup>

        <FieldGroup label="Meilisearch">
          <SettingField
            label="URL"
            value={form.getValue("catalog.search.meilisearch.url")}
            onChange={(value) => form.setValue("catalog.search.meilisearch.url", value)}
            hint="http://localhost:7700"
            disabled={!meiliEnabled}
          />
          <SettingField
            label="API Key"
            type="password"
            value={form.getValue("catalog.search.meilisearch.api_key")}
            onChange={(value) => form.setValue("catalog.search.meilisearch.api_key", value)}
            sensitiveConfigured={form.sensitiveConfigured.includes(
              "catalog.search.meilisearch.api_key",
            )}
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Index Prefix"
            value={form.getValue("catalog.search.meilisearch.index") || "silo_media_items"}
            onChange={(value) => form.setValue("catalog.search.meilisearch.index", value)}
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Timeout (ms)"
            type="number"
            value={form.getValue("catalog.search.meilisearch.timeout_ms") || "800"}
            onChange={(value) => form.setValue("catalog.search.meilisearch.timeout_ms", value)}
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Matching Strategy"
            type="select"
            value={form.getValue("catalog.search.meilisearch.matching_strategy") || "last"}
            onChange={(value) =>
              form.setValue("catalog.search.meilisearch.matching_strategy", value)
            }
            options={[
              { value: "last", label: "Last" },
              { value: "all", label: "All" },
            ]}
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Sync Batch Size"
            type="number"
            value={form.getValue("catalog.search.meilisearch.sync_batch_size") || "500"}
            onChange={(value) => form.setValue("catalog.search.meilisearch.sync_batch_size", value)}
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Rebuild Batch Size"
            type="number"
            value={form.getValue("catalog.search.meilisearch.rebuild_batch_size") || "5000"}
            onChange={(value) =>
              form.setValue("catalog.search.meilisearch.rebuild_batch_size", value)
            }
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Rebuild Queue Depth"
            type="number"
            value={form.getValue("catalog.search.meilisearch.rebuild_task_queue_depth") || "4"}
            onChange={(value) =>
              form.setValue("catalog.search.meilisearch.rebuild_task_queue_depth", value)
            }
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Indexed Types"
            value={form.getValue("catalog.search.meilisearch.index_types")}
            onChange={(value) => form.setValue("catalog.search.meilisearch.index_types", value)}
            hint="all, video, or movie,series"
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Semantic Search"
            type="toggle"
            value={form.getValue("catalog.search.meilisearch.semantic_enabled") || "false"}
            onChange={(value) =>
              form.setValue("catalog.search.meilisearch.semantic_enabled", value)
            }
            hint="Uses existing recommendation embeddings for hybrid catalog search."
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Semantic Ratio"
            type="number"
            value={form.getValue("catalog.search.meilisearch.semantic_ratio") || "0.50"}
            onChange={(value) => form.setValue("catalog.search.meilisearch.semantic_ratio", value)}
            hint="0.50"
            disabled={!meiliEnabled}
          />
          <SettingField
            label="Embedder"
            value={form.getValue("catalog.search.meilisearch.embedder") || "silo_recommendations"}
            onChange={(value) => form.setValue("catalog.search.meilisearch.embedder", value)}
            disabled={!meiliEnabled}
          />
          <div className="py-3">
            <ConnectionCheckAction
              onClick={handleCheckConnection}
              result={connectionResult}
              isPending={checkConnection.isPending}
              disabled={!meiliEnabled}
            />
          </div>
        </FieldGroup>

        <FieldGroup label="Status">
          {statusLoading || !status ? (
            <div className="space-y-3 py-3">
              <Skeleton className="h-5 w-56" />
              <Skeleton className="h-5 w-72" />
              <Skeleton className="h-5 w-48" />
            </div>
          ) : (
            <div className="divide-border divide-y">
              <StatusRow
                label="Active Provider"
                value={status.active_provider === "meilisearch" ? "Meilisearch" : "Postgres FTS"}
                badge={status.configured_provider}
              />
              <StatusRow
                label="Health"
                value={status.meilisearch.healthy ? "Healthy" : status.meilisearch.circuit_state}
                badge={status.meilisearch.configured ? "configured" : "not configured"}
              />
              <StatusRow
                label="Active Index"
                value={status.index.active_index_uid || "Not built"}
                badge={`schema ${status.index.schema_version}/${status.index.expected_schema_version}`}
              />
              <StatusRow label="Documents" value={String(status.index.document_count)} />
              <StatusRow
                label="Indexed Types"
                value={formatIndexedTypes(status.meilisearch.index_types)}
              />
              <StatusRow
                label="Semantic Search"
                value={status.meilisearch.semantic_enabled ? "Enabled" : "Disabled"}
                badge={status.meilisearch.embedder}
              />
              <StatusRow
                label="Semantic Ratio"
                value={formatSemanticRatio(status.meilisearch.semantic_ratio)}
              />
              <StatusRow
                label="Vectorized Documents"
                value={String(status.index.vector_document_count)}
              />
              {status.semantic && (
                <>
                  <StatusRow
                    label="Semantic Readiness"
                    value={status.semantic.ready ? "Ready" : "Not ready"}
                    badge={status.semantic.ready ? undefined : status.semantic.disabled_reason}
                  />
                  <StatusRow
                    label="Vector Coverage"
                    value={formatPercent(status.semantic.vector_coverage_ratio)}
                  />
                  <StatusRow
                    label="Coverage Updated"
                    value={formatStatusDate(status.semantic.coverage_updated_at) || "Never"}
                  />
                  <StatusRow
                    label="Embedder Capability"
                    value={
                      status.semantic.capability.ok
                        ? "OK"
                        : (status.semantic.capability.reason ?? "Unavailable")
                    }
                    badge={status.semantic.capability.embedder}
                  />
                  {status.semantic.per_type && status.semantic.per_type.length > 0 && (
                    <div className="flex flex-col gap-2 py-3">
                      <span className="text-sm font-medium">Per-Type Coverage</span>
                      <div className="divide-border/60 divide-y">
                        {status.semantic.per_type.map((t) => (
                          <div
                            key={t.type}
                            className="flex items-center justify-between gap-2 py-1.5"
                          >
                            <span className="text-muted-foreground min-w-0 truncate text-sm">
                              {t.type}: {t.vectorized}/{t.eligible} (
                              {formatPercent(t.vector_coverage_ratio)})
                            </span>
                            <Badge variant={t.ready ? "secondary" : "outline"}>
                              {t.ready ? "Ready" : "Not ready"}
                            </Badge>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                </>
              )}
              <StatusRow label="Pending Events" value={String(status.index.pending_events)} />
              {status.index.dead_lettered_events > 0 && (
                <StatusRow
                  label="Dead-lettered Events"
                  value={String(status.index.dead_lettered_events)}
                  badge="stale until rebuild"
                />
              )}
              <StatusRow
                label="Last Sync"
                value={formatStatusDate(status.index.last_sync_at) || "Never"}
              />
              {status.meilisearch.last_fallback && (
                <StatusRow label="Last Fallback" value={status.meilisearch.last_fallback} />
              )}
              <div className="flex flex-wrap gap-2 py-3">
                <Button asChild size="sm" variant="outline">
                  <Link to="/admin/tasks/rebuild_catalog_search_index">Rebuild Index</Link>
                </Button>
                <Button asChild size="sm" variant="ghost">
                  <Link to="/admin/tasks/sync_catalog_search_index">Sync History</Link>
                </Button>
              </div>
            </div>
          )}
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={form.restartRequired}
      />
    </div>
  );
}

function StatusRow({ label, value, badge }: { label: string; value: string; badge?: string }) {
  return (
    <div className="flex flex-col gap-2 py-3 sm:flex-row sm:items-center sm:justify-between">
      <span className="text-sm font-medium">{label}</span>
      <span className="text-muted-foreground flex min-w-0 flex-wrap items-center gap-2 text-sm">
        <span className="max-w-full text-right break-words">{value}</span>
        {badge ? <Badge variant="outline">{badge}</Badge> : null}
      </span>
    </div>
  );
}

function formatStatusDate(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString();
}

function formatIndexedTypes(value?: string[]) {
  if (!value || value.length === 0) return "All";
  return value.join(", ");
}

function formatSemanticRatio(value?: number) {
  if (typeof value !== "number" || Number.isNaN(value)) return "0";
  return value.toFixed(2);
}

function formatPercent(value?: number) {
  if (typeof value !== "number" || Number.isNaN(value)) return "0%";
  return `${Math.round(value * 100)}%`;
}
