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
import { useT } from "@/i18n";
import { SaveBar } from "@/components/SaveBar";
import { FieldGroup } from "./FieldGroup";
import { SearchStatusPanel, SearchStatusSummary } from "./SearchStatusPanel";
import { SettingField, SettingFieldStatus } from "./SettingField";

const PROVIDER_KEY = "catalog.search.provider";
const MEILI_URL_KEY = "catalog.search.meilisearch.url";
const MEILI_API_KEY = "catalog.search.meilisearch.api_key";

// The environment variable that sets each key on every server start; while it
// is set the server refuses saves of that key.
const ENV_VARS: Record<string, string> = {
  [PROVIDER_KEY]: "SILO_SEARCH_PROVIDER",
  [MEILI_URL_KEY]: "SILO_MEILISEARCH_URL",
  [MEILI_API_KEY]: "SILO_MEILISEARCH_API_KEY",
};

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

const MEILISEARCH = "Meilisearch";
const MEILI_URL_EXAMPLE = "http://localhost:7700";

export default function SearchSettings() {
  const t = useT();
  const form = useSettingsForm({ keys: KEYS });
  const restartKeys = useRestartKeys();
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);

  const provider = form.getValue(PROVIDER_KEY) || "postgres";
  const meiliEnabled = provider === "meilisearch";
  const envVar = (key: string) =>
    form.sensitiveManagedByEnv.includes(key) ? ENV_VARS[key] : undefined;
  const providerEnv = envVar(PROVIDER_KEY);
  const urlEnv = envVar(MEILI_URL_KEY);
  const apiKeyEnv = envVar(MEILI_API_KEY);
  // Without the protected-credential status the page cannot tell which of
  // these fields the environment sets, so they wait for a reload.
  const connectionUnknown = form.sensitiveStatusError;
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
        message:
          error instanceof Error
            ? error.message
            : t("web.admin.settings.fields.connectionCheckFailed"),
      });
    }
  }

  // Which fields the environment sets comes from the protected-credential
  // status, so the page waits for it rather than take an edit it must refuse.
  if (form.isLoading || (!form.sensitiveStatusReady && !form.sensitiveStatusError)) {
    return (
      <div
        className="space-y-6"
        role="status"
        aria-label={t("web.admin.settings.fields.loadingSettings")}
      >
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full" />
        <span className="sr-only">{t("web.admin.settings.fields.loadingSettings")}</span>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.search.title")} />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label={t("web.admin.settings.search.group")}
          description={t("web.admin.settings.search.description")}
          restartAll={KEYS.every((key) => restartKeys.has(key))}
          dirty={anyDirty(KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.search.provider.label")}
            type="select"
            description={
              providerEnv
                ? t("web.admin.settings.search.managedByEnv", { env: providerEnv })
                : t("web.admin.settings.search.provider.description")
            }
            value={provider}
            onChange={(value) => form.setValue(PROVIDER_KEY, value)}
            options={[
              { value: "postgres", label: t("web.admin.settings.search.provider.postgres") },
              { value: "meilisearch", label: MEILISEARCH },
            ]}
            status={meiliEnabled && !searchStatus?.degraded ? <SearchStatusSummary /> : undefined}
            disabled={providerEnv != null || connectionUnknown}
            restartRequired={restartKeys.has(PROVIDER_KEY)}
          />
          {connectionUnknown && (
            <div className="py-3.5">
              <SettingFieldStatus tone="warn">
                {t("web.admin.settings.search.credentialStatusError")}
              </SettingFieldStatus>
            </div>
          )}
          {meiliEnabled && searchStatus?.degraded && (
            <div className="py-3.5">
              <SettingFieldStatus tone="warn">
                <span>
                  {searchStatus.degraded_reason ?? t("web.admin.settings.search.degradedFallback")}
                  {searchStatus.index.rebuild_required && (
                    <>
                      {" "}
                      {t("web.admin.settings.search.rebuildInBackground", {
                        link: (chunks) => (
                          <Link
                            className="font-medium underline underline-offset-2"
                            to="/admin/tasks/sync_catalog_search_index"
                          >
                            {chunks}
                          </Link>
                        ),
                      })}
                    </>
                  )}
                </span>
              </SettingFieldStatus>
            </div>
          )}

          {showMeili && (
            <>
              <SettingField
                label={t("web.admin.settings.search.meili.url.label")}
                description={urlEnv && t("web.admin.settings.search.managedByEnv", { env: urlEnv })}
                value={form.getValue(MEILI_URL_KEY)}
                onChange={(value) => form.setValue(MEILI_URL_KEY, value)}
                hint={MEILI_URL_EXAMPLE}
                disabled={!meiliEnabled || urlEnv != null || connectionUnknown}
                restartRequired={restartKeys.has(MEILI_URL_KEY)}
              />
              <SecretField
                label={t("web.admin.settings.search.meili.apiKey.label")}
                value={form.getValue(MEILI_API_KEY)}
                configured={form.sensitiveConfigured.includes(MEILI_API_KEY)}
                onChange={(value) => form.setValue(MEILI_API_KEY, value)}
                onKeep={() => form.resetValue(MEILI_API_KEY)}
                // Nothing else on this page can empty the stored key, and a
                // Meilisearch instance without a master key needs it empty.
                onClear={() => form.setValue(MEILI_API_KEY, "")}
                cleared={form.isClearStaged(MEILI_API_KEY)}
                hint={
                  apiKeyEnv
                    ? t("web.admin.settings.search.managedByEnv", { env: apiKeyEnv })
                    : t("web.admin.settings.search.meili.apiKey.hint")
                }
                disabled={!meiliEnabled || apiKeyEnv != null || connectionUnknown}
                restartRequired={restartKeys.has(MEILI_API_KEY)}
              />
              <ConnectionCheckAction
                onClick={handleCheckConnection}
                result={connectionResult}
                isPending={checkConnection.isPending}
                disabled={!meiliEnabled}
              />
              <SettingField
                label={t("web.admin.settings.search.meili.matching.label")}
                type="select"
                value={form.getValue("catalog.search.meilisearch.matching_strategy") || "last"}
                onChange={(value) =>
                  form.setValue("catalog.search.meilisearch.matching_strategy", value)
                }
                options={[
                  { value: "last", label: t("web.admin.settings.search.meili.matching.last") },
                  { value: "all", label: t("web.admin.settings.search.meili.matching.all") },
                ]}
                disabled={!meiliEnabled}
                restartRequired={restartKeys.has("catalog.search.meilisearch.matching_strategy")}
              />
              <SettingField
                label={t("web.admin.settings.search.meili.semantic.label")}
                type="toggle"
                value={form.getValue("catalog.search.meilisearch.semantic_enabled") || "false"}
                onChange={(value) =>
                  form.setValue("catalog.search.meilisearch.semantic_enabled", value)
                }
                description={t("web.admin.settings.search.meili.semantic.description")}
                status={
                  enablingSemanticSearch ? (
                    <SettingFieldStatus tone="warn">
                      {t("web.admin.settings.search.meili.semantic.enablingWarning")}
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
                  label={t("web.admin.settings.search.meili.index.label")}
                  value={form.getValue("catalog.search.meilisearch.index") || "silo_media_items"}
                  onChange={(value) => form.setValue("catalog.search.meilisearch.index", value)}
                  description={t("web.admin.settings.search.meili.index.description")}
                  disabled={!meiliEnabled}
                  restartRequired={restartKeys.has("catalog.search.meilisearch.index")}
                />
                <SettingField
                  label={t("web.admin.settings.search.meili.timeout.label")}
                  type="number"
                  unit="ms"
                  value={form.getValue("catalog.search.meilisearch.timeout_ms") || "800"}
                  onChange={(value) =>
                    form.setValue("catalog.search.meilisearch.timeout_ms", value)
                  }
                  description={t("web.admin.settings.search.meili.timeout.description")}
                  disabled={!meiliEnabled}
                  restartRequired={restartKeys.has("catalog.search.meilisearch.timeout_ms")}
                />
                <SettingField
                  label={t("web.admin.settings.search.meili.batch.label")}
                  type="number"
                  value={form.getValue("catalog.search.meilisearch.sync_batch_size") || "500"}
                  onChange={(value) =>
                    form.setValue("catalog.search.meilisearch.sync_batch_size", value)
                  }
                  description={t("web.admin.settings.search.meili.batch.description")}
                  disabled={!meiliEnabled}
                  restartRequired={restartKeys.has("catalog.search.meilisearch.sync_batch_size")}
                />
                <SettingField
                  label={t("web.admin.settings.search.meili.ratio.label")}
                  type="number"
                  value={form.getValue("catalog.search.meilisearch.semantic_ratio") || "0.50"}
                  onChange={(value) =>
                    form.setValue("catalog.search.meilisearch.semantic_ratio", value)
                  }
                  description={t("web.admin.settings.search.meili.ratio.description")}
                  disabled={!meiliEnabled}
                  restartRequired={restartKeys.has("catalog.search.meilisearch.semantic_ratio")}
                />
              </>
            )}

            <SettingsSubheading>{t("web.admin.settings.search.status.heading")}</SettingsSubheading>
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
