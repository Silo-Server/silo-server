import { useState } from "react";
import { ArrowRight, RotateCcw } from "lucide-react";
import { Link } from "react-router";

import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
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
import { WORKER_SETTING_DEFAULTS, hasWorkerOverrides } from "./settingsWorkerDefaults";

const ARTWORK_KEYS = ["metadata.cache_images"];

const BROWSING_KEYS = ["catalog.scope_versions_to_library", "access.unrated_content"];

const SCANNER_KEYS = [
  "scanner.workers",
  "matcher.workers",
  "matcher.batch_size",
  "metadata.image_workers",
];

const MARKER_KEYS = [
  "markers.mode",
  "markers.lazy_playback",
  "markers.online_storage",
  "markers.detection_workers",
];

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
const KEYS = [...ARTWORK_KEYS, ...BROWSING_KEYS, ...SCANNER_KEYS, ...MARKER_KEYS, ...SEARCH_KEYS];

export default function LibraryMetadataSettings() {
  const form = useSettingsForm({ keys: KEYS });
  const restartKeys = useRestartKeys();
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);

  const provider = form.getValue("catalog.search.provider") || "postgres";
  const meiliEnabled = provider === "meilisearch";
  const { data: searchStatus } = useCatalogSearchStatus(meiliEnabled);
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  // Restoring stages every worker value at once; the save bar still confirms
  // it. The button is only offered while something differs from the default,
  // so an untouched group never shows a control that would do nothing.
  const workerOverrides = hasWorkerOverrides(form.getValue);
  function restoreWorkerDefaults() {
    for (const [key, fallback] of Object.entries(WORKER_SETTING_DEFAULTS)) {
      if (form.getValue(key) !== fallback) form.setValue(key, fallback);
    }
  }
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

  const markerMode = form.getValue("markers.mode") || "both";
  const onlineMarkersEnabled = markerMode === "online" || markerMode === "both";
  const onlineMarkerStorage = form.getValue("markers.online_storage") || "stored";

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
          description="Posters and backdrops from metadata providers, copied into your artwork storage."
          restartAll={allRestart(ARTWORK_KEYS)}
        >
          <SettingField
            label="Keep provider artwork"
            type="toggle"
            description="When off, clients load artwork straight from the providers."
            value={form.getValue("metadata.cache_images")}
            onChange={(value) => form.setValue("metadata.cache_images", value)}
            restartRequired={restartKeys.has("metadata.cache_images")}
          />
        </FieldGroup>

        <FieldGroup
          label="Browsing"
          description="How an item that lives in more than one library is shown."
          restartAll={allRestart(BROWSING_KEYS)}
        >
          <SettingField
            label="Show only the browsed library's versions"
            type="toggle"
            description="When an item is in several libraries, opening it from one library lists only the files stored there. Off, every version the viewer can access is listed no matter where they opened it. Playback always sees every version."
            value={form.getValue("catalog.scope_versions_to_library") || "false"}
            onChange={(value) => form.setValue("catalog.scope_versions_to_library", value)}
            restartRequired={restartKeys.has("catalog.scope_versions_to_library")}
          />
          <SettingField
            label="Titles with no age rating"
            type="select"
            description="Applies to profiles with a maturity ceiling: whether they see titles with no rating, or marked Not Rated. A rating the server cannot read stays hidden from them either way. Profiles without a ceiling always see these titles."
            value={form.getValue("access.unrated_content") || "hide"}
            onChange={(value) => form.setValue("access.unrated_content", value)}
            options={[
              { value: "hide", label: "Hide from profiles with a ceiling" },
              { value: "allow", label: "Show to every profile" },
            ]}
            restartRequired={restartKeys.has("access.unrated_content")}
          />
        </FieldGroup>

        <FieldGroup
          label="Scanning"
          restartAll={allRestart(SCANNER_KEYS)}
          dirty={anyDirty(SCANNER_KEYS)}
          actions={
            workerOverrides ? (
              <Button
                type="button"
                variant="ghost"
                size="xs"
                className="text-muted-foreground hover:text-foreground"
                onClick={restoreWorkerDefaults}
              >
                <RotateCcw aria-hidden="true" />
                Restore defaults
              </Button>
            ) : undefined
          }
        >
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
              label="Image encoding workers"
              type="number"
              description="How many artwork images Silo encodes at once. 0 uses one per CPU core."
              value={form.getValue("metadata.image_workers")}
              onChange={(value) => form.setValue("metadata.image_workers", value)}
              restartRequired={restartKeys.has("metadata.image_workers")}
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
          label="Skip markers"
          description="Markers identify intros, credits, recaps, and previews so players can offer skip controls."
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
            label="Marker source"
            type="select"
            description="Online markers take priority. Silo skips local intro detection when an online intro is saved in your library. Local detection uses CPU."
            className="[&_[data-slot=select-trigger]]:h-auto [&_[data-slot=select-trigger]]:min-h-9 [&_[data-slot=select-value]]:line-clamp-none [&_[data-slot=select-value]]:text-left [&_[data-slot=select-value]]:whitespace-normal"
            options={[
              { value: "off", label: "Off" },
              { value: "local", label: "Detect on this server" },
              { value: "both", label: "Online preferred + server detection" },
              { value: "online", label: "Online providers only" },
            ]}
            value={markerMode}
            onChange={(value) => form.setValue("markers.mode", value)}
            restartRequired={restartKeys.has("markers.mode")}
          />

          {onlineMarkersEnabled && (
            <SettingField
              label="Save online markers"
              type="select"
              description={
                onlineMarkerStorage === "stored"
                  ? "Silo saves markers from enabled providers such as TheIntroDB. The Sync online markers task fetches missing markers and refreshes saved markers daily at 03:00 (server time) by default."
                  : "Fetch markers when needed without saving them to your library. Scheduled online sync is disabled."
              }
              options={[
                { value: "stored", label: "Save to library" },
                { value: "on_demand", label: "Fetch when needed" },
              ]}
              value={onlineMarkerStorage}
              onChange={(value) => {
                form.setValue("markers.online_storage", value);
                if (value === "on_demand") form.setValue("markers.lazy_playback", "true");
              }}
              restartRequired={restartKeys.has("markers.online_storage")}
            />
          )}

          {markerMode !== "off" && (!onlineMarkersEnabled || onlineMarkerStorage === "stored") && (
            <SettingField
              label="Find markers on playback"
              type="toggle"
              description={
                onlineMarkersEnabled
                  ? markerMode === "both"
                    ? "Check online first when playback starts. If intro or credits markers are available, skip local detection. Otherwise, detect locally using this server's CPU."
                    : "Check online for missing or outdated markers when playback starts."
                  : "Detect missing markers when playback starts. Local analysis uses CPU."
              }
              value={form.getValue("markers.lazy_playback") || "true"}
              onChange={(value) => form.setValue("markers.lazy_playback", value)}
              restartRequired={restartKeys.has("markers.lazy_playback")}
            />
          )}

          {(markerMode === "local" || markerMode === "both") && (
            <SettingField
              label="Detection workers"
              type="number"
              description="How many seasons Silo analyzes for intros at once, each reading audio with its own ffmpeg process. Defaults to 1. Raise it to finish a large library sooner if your storage and CPU have room."
              value={form.getValue("markers.detection_workers")}
              onChange={(value) => form.setValue("markers.detection_workers", value)}
              restartRequired={restartKeys.has("markers.detection_workers")}
            />
          )}

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
