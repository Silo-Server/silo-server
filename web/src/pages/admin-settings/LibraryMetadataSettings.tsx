import { useMemo, useState } from "react";
import { AlertTriangle, ArrowRight } from "lucide-react";
import { Link } from "react-router";

import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Skeleton } from "@/components/ui/skeleton";
import { useBranding } from "@/hooks/useBranding";
import {
  useCatalogSearchStatus,
  useCheckAdminSettingsConnection,
} from "@/hooks/queries/admin/settings";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { FieldGroup } from "./FieldGroup";
import { MarkerTasksCard } from "./MarkerTasksCard";
import { SaveBar } from "./SaveBar";
import { SearchStatusPanel } from "./SearchStatusPanel";
import { SettingField, SettingFieldStatus } from "./SettingField";

const ARTWORK_KEYS = ["metadata.cache_images"];

const SCANNER_KEYS = ["scanner.workers", "matcher.workers", "matcher.batch_size"];

const MARKER_KEYS = ["markers.mode", "markers.lazy_playback"];

const MEILI_URL_KEY = "catalog.search.meilisearch.url";
const MEILI_API_KEY = "catalog.search.meilisearch.api_key";

const MEILI_ADVANCED_KEYS = [
  "catalog.search.meilisearch.index",
  "catalog.search.meilisearch.timeout_ms",
  "catalog.search.meilisearch.matching_strategy",
  "catalog.search.meilisearch.sync_batch_size",
  "catalog.search.meilisearch.semantic_enabled",
  "catalog.search.meilisearch.semantic_ratio",
];

const MEILI_KEYS = [MEILI_URL_KEY, MEILI_API_KEY, ...MEILI_ADVANCED_KEYS];

const SEARCH_KEYS = ["catalog.search.provider", ...MEILI_KEYS];

// Hidden tier: still saved and readable through the settings API, deliberately
// without a control here because the defaults are right for every deployment we
// support — catalog.search.meilisearch.{rebuild_batch_size,
// rebuild_task_queue_depth,index_types,embedder,binary_quantized}.
const KEYS = [...ARTWORK_KEYS, ...SCANNER_KEYS, ...MARKER_KEYS, ...SEARCH_KEYS];

export default function LibraryMetadataSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const branding = useBranding();
  const restartKeys = useRestartKeys();
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);

  // Artwork storage writes provider images into the public bucket, so the
  // server rejects enabling it when no bucket is configured at all.
  // `storage_available` is the process-level truth (branding uses the same
  // flag for asset uploads); s3.public_bucket only says a bucket was saved,
  // which is enough for the server to accept the save — the two together
  // separate "restart pending" from "never configured". s3.public_bucket is
  // not staged here, but getValue falls back to the full settings response.
  const publicBucketSaved = Boolean(form.getValue("s3.public_bucket"));
  const artworkStorageOn = form.getValue("metadata.cache_images") === "true";
  // Never trap an admin with it on: turning it off stays available even when
  // the bucket went away.
  const artworkStorageLocked =
    !branding.storageAvailable && !publicBucketSaved && !artworkStorageOn;

  const provider = form.getValue("catalog.search.provider") || "postgres";
  const meiliEnabled = provider === "meilisearch";
  const { data: searchStatus } = useCatalogSearchStatus(meiliEnabled);
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
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

  const markerMode = form.getValue("markers.mode") || "local";

  if (form.isLoading) {
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-40 w-full" />
        <span className="sr-only">Loading settings</span>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Library & Metadata" className="mb-8" />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label="Artwork"
          description="Posters and backdrops from metadata providers, copied into the public bucket and served from there."
          restartAll={allRestart(ARTWORK_KEYS)}
        >
          {!branding.storageAvailable && (
            <div className="mt-3 flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
              <p className="text-muted-foreground text-[13px] leading-relaxed">
                {publicBucketSaved ? (
                  <>Restart the server for artwork storage to start.</>
                ) : (
                  <>
                    Artwork storage needs a public S3 bucket, set in{" "}
                    <Link
                      to="/admin/settings/infrastructure"
                      className="text-foreground font-medium underline-offset-2 hover:underline"
                    >
                      Storage &amp; Database
                    </Link>{" "}
                    settings.
                  </>
                )}
              </p>
            </div>
          )}
          <SettingField
            label="Store artwork in your bucket"
            type="toggle"
            description="When off, clients load artwork straight from the providers."
            value={form.getValue("metadata.cache_images")}
            onChange={(value) => form.setValue("metadata.cache_images", value)}
            disabled={artworkStorageLocked}
            restartRequired={restartKeys.has("metadata.cache_images")}
          />
        </FieldGroup>

        <FieldGroup label="Scanning" restartAll={allRestart(SCANNER_KEYS)}>
          <AdvancedSection
            id="library.scanning"
            count={SCANNER_KEYS.length}
            forceOpen={anyDirty(SCANNER_KEYS)}
          >
            <SettingField
              label="Scanner workers"
              type="number"
              description="How many files Silo reads at once."
              value={form.getValue("scanner.workers")}
              onChange={(value) => form.setValue("scanner.workers", value)}
              restartRequired={restartKeys.has("scanner.workers")}
            />
            <SettingField
              label="Matcher workers"
              type="number"
              description="How many items Silo looks up at once."
              value={form.getValue("matcher.workers")}
              onChange={(value) => form.setValue("matcher.workers", value)}
              restartRequired={restartKeys.has("matcher.workers")}
            />
            <SettingField
              label="Matcher batch size"
              type="number"
              description="How many items each matcher worker claims per round."
              value={form.getValue("matcher.batch_size")}
              onChange={(value) => form.setValue("matcher.batch_size", value)}
              restartRequired={restartKeys.has("matcher.batch_size")}
            />
          </AdvancedSection>
        </FieldGroup>

        {/*
          Detection behavior lives here; which online providers answer a lookup,
          and on what terms, is provider configuration and lives with the other
          providers.
        */}
        <FieldGroup
          label="Intro and credits markers"
          restartAll={allRestart(MARKER_KEYS)}
          actions={
            <Link
              to="/admin/settings/providers"
              className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
            >
              Marker providers
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          }
        >
          <SettingField
            label="Find intros and credits"
            type="select"
            description="Detecting on this server utilizes CPU. Looking online uses the marker providers set up on the Subtitles & Metadata page."
            options={[
              { value: "off", label: "Off" },
              { value: "local", label: "Detect on this server" },
              { value: "both", label: "Detect on this server, then look online" },
              { value: "online", label: "Look online only" },
            ]}
            value={markerMode}
            onChange={(value) => form.setValue("markers.mode", value)}
            restartRequired={restartKeys.has("markers.mode")}
          />

          <SettingField
            label="Fetch markers on playback"
            type="toggle"
            description="Uses the enabled marker providers to look up missing markers when playback starts. Can delay the first few seconds."
            value={form.getValue("markers.lazy_playback") || "false"}
            onChange={(value) => form.setValue("markers.lazy_playback", value)}
            restartRequired={restartKeys.has("markers.lazy_playback")}
          />

          <div className="py-3.5">
            <MarkerTasksCard />
          </div>
        </FieldGroup>

        <FieldGroup label="Search" restartAll={allRestart(SEARCH_KEYS)}>
          <SettingField
            label="Search engine"
            type="select"
            description="Meilisearch tolerates typos but runs as its own service. If it goes down, search falls back to the built-in engine automatically."
            value={provider}
            onChange={(value) => form.setValue("catalog.search.provider", value)}
            options={[
              { value: "postgres", label: "Built-in (Postgres)" },
              { value: "meilisearch", label: "Meilisearch" },
            ]}
            restartRequired={restartKeys.has("catalog.search.provider")}
          />

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

              <AdvancedSection
                id="library.search.meilisearch"
                count={MEILI_ADVANCED_KEYS.length}
                forceOpen={anyDirty(MEILI_ADVANCED_KEYS)}
              >
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
              </AdvancedSection>
            </>
          )}

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

          <AdvancedSection id="library.search.status" title="Search status">
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
