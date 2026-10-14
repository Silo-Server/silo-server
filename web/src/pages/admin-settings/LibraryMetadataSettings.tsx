import { useQueryClient } from "@tanstack/react-query";
import { RotateCcw } from "lucide-react";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import {
  useAdminRatingSourceCapabilities,
  useAdminRatingSources,
} from "@/hooks/queries/admin/ratingSources";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { FieldGroup } from "./FieldGroup";
import {
  EXTRA_RATING_SOURCES_KEY,
  groupRatingSourcesByPlugin,
  parseRatingSources,
  saveAndRefreshRatings,
  toggleRatingSource,
  undeclaredRatingSources,
} from "./ratingSources";
import { SaveBar } from "@/components/SaveBar";
import {
  SUBTITLE_SYNC_EXECUTION_DEFAULT,
  hasUsableTranscodeNode,
  isNodeBackedExecution,
  subtitleSyncExecutionOptions,
} from "./playbackSettings.utils";
import { SettingField, SettingFieldStatus } from "./SettingField";
import { WORKER_SETTING_DEFAULTS, hasWorkerOverrides } from "./settingsWorkerDefaults";

// Skip markers are on Playback, search on its own Search page, and saving
// provider artwork on Storage; none of their keys are loaded here.
const GENERAL_KEYS = ["catalog.scope_versions_to_library"];

// Not a browsing preference: a rule about what a profile with a maximum
// content rating may see at all.
const MATURITY_KEYS = ["access.unrated_content"];

const RATINGS_KEYS = [EXTRA_RATING_SOURCES_KEY];

// How many files, lookups, and images run at once is what decides scan speed
// on a given machine, so the three worker counts are shown; the person lookup
// rate and the matcher batch are tuning behind Advanced.
const SCANNER_ADVANCED_KEYS = ["metadata.person_refresh_per_minute", "matcher.batch_size"];

const SCANNER_KEYS = [
  "scanner.workers",
  "matcher.workers",
  "metadata.image_workers",
  ...SCANNER_ADVANCED_KEYS,
];

// Each library turns real-time monitoring on or off in its own settings, so the
// server-wide switch only appears while it is off: the library editor sends an
// admin here to turn it back on.
const REALTIME_MONITORING_KEY = "scanner.realtime_monitoring";

const SCANNING_GROUP_KEYS = [REALTIME_MONITORING_KEY, ...SCANNER_KEYS];

// Subtitle sync aligns a subtitle file that is already there, so it sits with
// the other per-file analysis here rather than with the subtitle providers that
// supply the files. Where the audio is decoded is placement, behind Advanced.
const SUBTITLE_SYNC_ADVANCED_KEYS = ["subtitles.sync_execution", "subtitles.sync_node_capacity"];

const SUBTITLE_SYNC_KEYS = ["subtitles.auto_sync", ...SUBTITLE_SYNC_ADVANCED_KEYS];

const KEYS = [
  ...GENERAL_KEYS,
  ...SCANNING_GROUP_KEYS,
  ...MATURITY_KEYS,
  ...RATINGS_KEYS,
  ...SUBTITLE_SYNC_KEYS,
];

export default function LibraryMetadataSettings() {
  const form = useSettingsForm({ keys: KEYS });
  const restartKeys = useRestartKeys();
  // An older API node has no rating source list, so the Ratings group shows
  // only where the server says it has one.
  const { data: ratingSourceCapabilities } = useAdminRatingSourceCapabilities();
  const pluginRatingSources = ratingSourceCapabilities?.plugin_declared_sources === true;
  const ratingSources = useAdminRatingSources(pluginRatingSources);
  const queryClient = useQueryClient();

  // A saved rating choice also refreshes cached title pages and cards.
  const handleSave = () => saveAndRefreshRatings(form, queryClient);
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  // Restoring stages every worker value at once; the save bar still confirms
  // it. The button is only offered while something differs from the default,
  // so an untouched group never shows a control that would do nothing.
  const workerOverrides = hasWorkerOverrides(form.getValue);
  const showRealtimeSwitch =
    form.getPersistedValue(REALTIME_MONITORING_KEY) === "false" ||
    form.isDirty(REALTIME_MONITORING_KEY);
  function restoreWorkerDefaults() {
    for (const [key, fallback] of Object.entries(WORKER_SETTING_DEFAULTS)) {
      if (form.getValue(key) !== fallback) form.setValue(key, fallback);
    }
  }

  const extraRatingSources = parseRatingSources(form.getValue(EXTRA_RATING_SOURCES_KEY));
  const ratingSourceGroups = groupRatingSourcesByPlugin(ratingSources.data?.items ?? []);
  // Listed from the saved value too, so a switch turned off stays on the page
  // until the change is saved.
  const undeclaredSources = ratingSources.isSuccess
    ? undeclaredRatingSources(
        [
          ...parseRatingSources(form.getPersistedValue(EXTRA_RATING_SOURCES_KEY)),
          ...extraRatingSources,
        ],
        ratingSources.data.items,
      )
    : [];
  const setRatingSource = (source: string, on: boolean) =>
    form.setValue(
      EXTRA_RATING_SOURCES_KEY,
      toggleRatingSource(form.getValue(EXTRA_RATING_SOURCES_KEY), source, on),
    );

  // Gate the node-backed sync modes only on a node list we actually have:
  // while the query is in flight or after it failed, leave every option
  // reachable rather than blocking a valid choice on a transient error.
  const nodes = useAdminNodes();
  const transcodeNodeAvailable = !nodes.isSuccess || hasUsableTranscodeNode(nodes.data);
  const syncExecution =
    form.getValue("subtitles.sync_execution") || SUBTITLE_SYNC_EXECUTION_DEFAULT;
  const syncOnNodes = isNodeBackedExecution(syncExecution);
  // An unsaved capacity stays in view after placement moves off the nodes, so
  // the save bar never counts a change the admin cannot see.
  const showSyncCapacity = syncOnNodes || form.isDirty("subtitles.sync_node_capacity");

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
      <SettingsPageHeader title="Library & Metadata" />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label="General"
          restartAll={allRestart(GENERAL_KEYS)}
          dirty={anyDirty(GENERAL_KEYS)}
        >
          {/* Shown as the opposite of the stored key: on is the default, where
              catalog.scope_versions_to_library is false. */}
          <SettingField
            label="Show all library versions on detail page"
            type="toggle"
            description="When a title is in several libraries, its detail page lists every version the viewer can access, wherever they opened it. Off, it lists only the files in the library they opened it from. Playback always sees every version."
            value={form.getValue("catalog.scope_versions_to_library") === "true" ? "false" : "true"}
            onChange={(value) =>
              form.setValue(
                "catalog.scope_versions_to_library",
                value === "true" ? "false" : "true",
              )
            }
            restartRequired={restartKeys.has("catalog.scope_versions_to_library")}
          />
        </FieldGroup>

        <FieldGroup
          label="Scanning"
          restartAll={allRestart(SCANNING_GROUP_KEYS)}
          dirty={anyDirty(SCANNING_GROUP_KEYS)}
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
          {showRealtimeSwitch && (
            <SettingField
              label="Real-time monitoring"
              type="toggle"
              description="Off for every library. When on, each library's own switch decides whether its folders are watched."
              value={form.getValue(REALTIME_MONITORING_KEY) || "true"}
              onChange={(value) => form.setValue(REALTIME_MONITORING_KEY, value)}
              restartRequired={restartKeys.has(REALTIME_MONITORING_KEY)}
            />
          )}
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
            label="Image encoding workers"
            type="number"
            description="How many artwork images Silo encodes at once. 0 uses one per CPU core."
            value={form.getValue("metadata.image_workers")}
            onChange={(value) => form.setValue("metadata.image_workers", value)}
            restartRequired={restartKeys.has("metadata.image_workers")}
          />
          <AdvancedSection
            id="library.scanning"
            count={SCANNER_ADVANCED_KEYS.length}
            forceOpen={anyDirty(SCANNER_ADVANCED_KEYS)}
          >
            <SettingField
              label="Person lookups per minute"
              type="number"
              description="How many cast and crew members each server looks up per minute in the background. Opening a person's page isn't limited."
              value={form.getValue("metadata.person_refresh_per_minute")}
              onChange={(value) => form.setValue("metadata.person_refresh_per_minute", value)}
              restartRequired={restartKeys.has("metadata.person_refresh_per_minute")}
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

        <FieldGroup
          label="Maturity limits"
          description="For profiles with a maximum content rating, which is set on each profile."
          restartAll={allRestart(MATURITY_KEYS)}
        >
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

        {pluginRatingSources && (
          <FieldGroup
            label="Ratings"
            description="IMDb and TMDB scores always show. Metadata providers can add other ratings; turn one on to show its scores on title pages in every app. A title page shows at most three ratings, in this order, so a rating turned on shows only where fewer than three come before it. Rotten Tomatoes scores also show on poster badges. Check a rating's terms before you turn it on: some, such as Rotten Tomatoes, restrict how others may display their scores."
            restartAll={allRestart(RATINGS_KEYS)}
          >
            {ratingSources.isError && (
              <SettingFieldStatus tone="warn">Couldn't load the rating sources.</SettingFieldStatus>
            )}
            {ratingSources.isSuccess &&
              ratingSourceGroups.length === 0 &&
              undeclaredSources.length === 0 && (
                <SettingFieldStatus tone="muted">
                  No metadata plugin adds ratings.
                </SettingFieldStatus>
              )}
            {ratingSourceGroups.map(({ provider, sources }) => (
              <div key={provider} role="group" aria-label={`From ${provider}`}>
                <p className="text-muted-foreground pt-2 text-xs font-semibold">From {provider}</p>
                {sources.map(({ source, label }) => (
                  <SettingField
                    key={source}
                    label={label}
                    type="toggle"
                    value={String(extraRatingSources.includes(source))}
                    onChange={(value) => setRatingSource(source, value === "true")}
                    restartRequired={restartKeys.has(EXTRA_RATING_SOURCES_KEY)}
                  />
                ))}
              </div>
            ))}
            {undeclaredSources.length > 0 && (
              <div role="group" aria-label="Not added by an enabled plugin">
                <p className="text-muted-foreground pt-2 text-xs font-semibold">
                  Not added by an enabled plugin
                </p>
                <p className="text-muted-foreground text-xs">
                  These show nothing until an enabled plugin adds them again. Turn them off so they
                  do not come back on their own.
                </p>
                {undeclaredSources.map((source) => (
                  <SettingField
                    key={source}
                    label={source}
                    type="toggle"
                    value={String(extraRatingSources.includes(source))}
                    onChange={(value) => setRatingSource(source, value === "true")}
                    restartRequired={restartKeys.has(EXTRA_RATING_SOURCES_KEY)}
                  />
                ))}
              </div>
            )}
          </FieldGroup>
        )}

        <FieldGroup
          label="Subtitle sync"
          description="Shifts subtitle timing to match the video's audio."
          restartAll={allRestart(SUBTITLE_SYNC_KEYS)}
          dirty={anyDirty(SUBTITLE_SYNC_KEYS)}
        >
          <SettingField
            label="Sync subtitles automatically"
            type="toggle"
            description="Aligns subtitles to the video's audio: downloaded and uploaded ones when they're added, any other the first time it's played. Fixes subtitles cut for a different release."
            dirty={form.isDirty("subtitles.auto_sync")}
            value={form.getValue("subtitles.auto_sync") || "true"}
            onChange={(value) => form.setValue("subtitles.auto_sync", value)}
            restartRequired={restartKeys.has("subtitles.auto_sync")}
          />
          <AdvancedSection
            id="library.subtitle_sync"
            count={showSyncCapacity ? 2 : 1}
            forceOpen={
              anyDirty(SUBTITLE_SYNC_ADVANCED_KEYS) ||
              // A node-only mode with no node to run on is stranded work;
              // its warning must not sit inside a closed disclosure.
              (!transcodeNodeAvailable && syncExecution === "transcode_nodes_only")
            }
          >
            <SettingField
              label="Where to analyze audio"
              type="select"
              description="Sync decodes a few minutes of the file's audio."
              options={subtitleSyncExecutionOptions(syncExecution, transcodeNodeAvailable)}
              status={
                !transcodeNodeAvailable && syncOnNodes ? (
                  <SettingFieldStatus tone="warn">
                    No transcode nodes are connected
                  </SettingFieldStatus>
                ) : undefined
              }
              dirty={form.isDirty("subtitles.sync_execution")}
              value={syncExecution}
              onChange={(value) => form.setValue("subtitles.sync_execution", value)}
              restartRequired={restartKeys.has("subtitles.sync_execution")}
            />
            {showSyncCapacity && (
              <SettingField
                label="Concurrent syncs per transcode node"
                type="number"
                description="How many subtitles one transcode node syncs at once."
                dirty={form.isDirty("subtitles.sync_node_capacity")}
                value={form.getValue("subtitles.sync_node_capacity") || "1"}
                onChange={(value) => form.setValue("subtitles.sync_node_capacity", value)}
                restartRequired={restartKeys.has("subtitles.sync_node_capacity")}
              />
            )}
          </AdvancedSection>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={handleSave}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
