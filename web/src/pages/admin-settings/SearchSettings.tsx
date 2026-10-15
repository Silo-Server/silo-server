import { useState } from "react";
import { Link } from "react-router";

import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCatalogSearchStatus,
  useCheckAdminSettingsConnection,
} from "@/hooks/queries/admin/settings";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SaveBar } from "@/components/SaveBar";
import { FieldGroup } from "./FieldGroup";
import { SearchStatusPanel, SearchStatusSummary } from "./SearchStatusPanel";
import { SettingField, SettingFieldStatus } from "./SettingField";

const PROVIDER_KEY = "catalog.search.provider";
const MEILI_URL_KEY = "catalog.search.meilisearch.url";
const MEILI_API_KEY = "catalog.search.meilisearch.api_key";

// Reaching Meilisearch and the two choices that change what a search returns
// are shown once it is selected; the index name, timing and ranking mix are
// tuning behind Advanced.
const MEILI_SHOWN_KEYS = [
  MEILI_URL_KEY,
  MEILI_API_KEY,
  "catalog.search.meilisearch.matching_strategy",
  "catalog.search.meilisearch.semantic_enabled",
];

const MEILI_ADVANCED_KEYS = [
  "catalog.search.meilisearch.index",
  "catalog.search.meilisearch.timeout_ms",
  "catalog.search.meilisearch.sync_batch_size",
  "catalog.search.meilisearch.semantic_ratio",
];

const MEILI_KEYS = [...MEILI_SHOWN_KEYS, ...MEILI_ADVANCED_KEYS];

// Hidden tier: still saved and readable through the settings API, deliberately
// without a control here because the defaults are right for every deployment we
// support — catalog.search.meilisearch.{rebuild_batch_size,
// rebuild_task_queue_depth,index_types,embedder,binary_quantized}.
const KEYS = [PROVIDER_KEY, ...MEILI_KEYS];

export default function SearchSettings() {
  const form = useSettingsForm({ keys: KEYS });
  const restartKeys = useRestartKeys();
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);

  const provider = form.getValue(PROVIDER_KEY) || "postgres";
  const meiliEnabled = provider === "meilisearch";
  const { data: searchStatus } = useCatalogSearchStatus(meiliEnabled);
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  // Staged Meilisearch edits stay reachable after switching the provider back,
  // so the save bar can never count a change the admin cannot see.
  const showMeili = meiliEnabled || anyDirty(MEILI_KEYS);
  const enablingSemanticSearch =
    form.isDirty("catalog.search.meilisearch.semantic_enabled") &&
    form.getPersistedValue("catalog.search.meilisearch.semantic_enabled") !== "true" &&
    form.getValue("catalog.search.meilisearch.semantic_enabled") === "true";

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
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full" />
        <span className="sr-only">Loading settings</span>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Search" />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label="Search"
          description="Built-in Postgres search needs no setup. Meilisearch is optional."
          restartAll={KEYS.every((key) => restartKeys.has(key))}
          dirty={anyDirty(KEYS)}
        >
          <SettingField
            label="Search engine"
            type="select"
            description="Meilisearch tolerates typos but runs as its own service. If it goes down, search falls back to the built-in engine automatically."
            value={provider}
            onChange={(value) => form.setValue(PROVIDER_KEY, value)}
            options={[
              { value: "postgres", label: "Built-in (Postgres)" },
              { value: "meilisearch", label: "Meilisearch" },
            ]}
            status={meiliEnabled && !searchStatus?.degraded ? <SearchStatusSummary /> : undefined}
            restartRequired={restartKeys.has(PROVIDER_KEY)}
          />
          {meiliEnabled && searchStatus?.degraded && (
            <div className="py-3.5">
              <SettingFieldStatus tone="warn">
                <span>
                  {searchStatus.degraded_reason ?? "Search is running in a degraded mode."}
                  {searchStatus.index.rebuild_required && (
                    <>
                      {" "}
                      Automatic search maintenance rebuilds the index in the background and retries
                      if needed.{" "}
                      <Link
                        className="font-medium underline underline-offset-2"
                        to="/admin/tasks/sync_catalog_search_index"
                      >
                        Open maintenance task
                      </Link>
                      .
                    </>
                  )}
                </span>
              </SettingFieldStatus>
            </div>
          )}

          {showMeili && (
            <>
              <SettingField
                label="Meilisearch URL"
                value={form.getValue(MEILI_URL_KEY)}
                onChange={(value) => form.setValue(MEILI_URL_KEY, value)}
                hint="http://localhost:7700"
                disabled={!meiliEnabled}
                restartRequired={restartKeys.has(MEILI_URL_KEY)}
              />
              <SecretField
                label="Meilisearch API key"
                value={form.getValue(MEILI_API_KEY)}
                configured={form.sensitiveConfigured.includes(MEILI_API_KEY)}
                onChange={(value) => form.setValue(MEILI_API_KEY, value)}
                onKeep={() => form.resetValue(MEILI_API_KEY)}
                // Nothing else on this page can empty the stored key, and a
                // Meilisearch instance without a master key needs it empty.
                onClear={() => form.setValue(MEILI_API_KEY, "")}
                cleared={form.isClearStaged(MEILI_API_KEY)}
                hint="Master key, or one that can read and write the index."
                disabled={!meiliEnabled}
                restartRequired={restartKeys.has(MEILI_API_KEY)}
              />
              <ConnectionCheckAction
                onClick={handleCheckConnection}
                result={connectionResult}
                isPending={checkConnection.isPending}
                disabled={!meiliEnabled}
              />
              <SettingField
                label="When a search has several words"
                type="select"
                value={form.getValue("catalog.search.meilisearch.matching_strategy") || "last"}
                onChange={(value) =>
                  form.setValue("catalog.search.meilisearch.matching_strategy", value)
                }
                options={[
                  { value: "last", label: "Drop trailing words until something matches" },
                  { value: "all", label: "Require every word" },
                ]}
                disabled={!meiliEnabled}
                restartRequired={restartKeys.has("catalog.search.meilisearch.matching_strategy")}
              />
              <SettingField
                label="Match by meaning as well as words"
                type="toggle"
                value={form.getValue("catalog.search.meilisearch.semantic_enabled") || "false"}
                onChange={(value) =>
                  form.setValue("catalog.search.meilisearch.semantic_enabled", value)
                }
                description="Also matches items whose description means something similar."
                status={
                  enablingSemanticSearch ? (
                    <SettingFieldStatus tone="warn">
                      Enabling this changes the index format. After you save and restart, Silo
                      rebuilds the index automatically. Keyword search stays available while it
                      rebuilds.
                    </SettingFieldStatus>
                  ) : undefined
                }
                disabled={!meiliEnabled}
                restartRequired={restartKeys.has("catalog.search.meilisearch.semantic_enabled")}
              />
            </>
          )}

          {/* The status panel is not a setting, so it is not counted; with
              Postgres the disclosure holds nothing else. */}
          <AdvancedSection
            id="search"
            count={showMeili ? MEILI_ADVANCED_KEYS.length : undefined}
            forceOpen={anyDirty(MEILI_ADVANCED_KEYS)}
          >
            {showMeili && (
              <>
                <SettingField
                  label="Index name prefix"
                  value={form.getValue("catalog.search.meilisearch.index") || "silo_media_items"}
                  onChange={(value) => form.setValue("catalog.search.meilisearch.index", value)}
                  description="Only needed when Silo servers share one Meilisearch."
                  disabled={!meiliEnabled}
                  restartRequired={restartKeys.has("catalog.search.meilisearch.index")}
                />
                <SettingField
                  label="Query timeout"
                  type="number"
                  unit="ms"
                  value={form.getValue("catalog.search.meilisearch.timeout_ms") || "800"}
                  onChange={(value) =>
                    form.setValue("catalog.search.meilisearch.timeout_ms", value)
                  }
                  description="Searches that take longer fall back to the built-in engine."
                  disabled={!meiliEnabled}
                  restartRequired={restartKeys.has("catalog.search.meilisearch.timeout_ms")}
                />
                <SettingField
                  label="Items sent to the index per batch"
                  type="number"
                  value={form.getValue("catalog.search.meilisearch.sync_batch_size") || "500"}
                  onChange={(value) =>
                    form.setValue("catalog.search.meilisearch.sync_batch_size", value)
                  }
                  description="Larger batches index faster and use more memory."
                  disabled={!meiliEnabled}
                  restartRequired={restartKeys.has("catalog.search.meilisearch.sync_batch_size")}
                />
                <SettingField
                  label="Meaning-based share of results"
                  type="number"
                  value={form.getValue("catalog.search.meilisearch.semantic_ratio") || "0.50"}
                  onChange={(value) =>
                    form.setValue("catalog.search.meilisearch.semantic_ratio", value)
                  }
                  description="0 ranks by words, 1 by meaning."
                  disabled={!meiliEnabled}
                  restartRequired={restartKeys.has("catalog.search.meilisearch.semantic_ratio")}
                />
              </>
            )}

            <SettingsSubheading>Search status</SettingsSubheading>
            <SearchStatusPanel />
          </AdvancedSection>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
