import { SaveBar } from "@/components/SaveBar";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { Button } from "@/components/ui/button";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT, type MessageKey, type Translate } from "@/i18n";

import { FieldGroup } from "./FieldGroup";
import { SettingField, SettingFieldRow, SettingFieldStatus } from "./SettingField";
import { hasUsableTranscodeNode } from "./playbackSettings.utils";
// The settings layout loads these field styles for its pages; the Nodes page
// is not one of them.
import "@/styles/admin-settings.css";

interface RoutingOption {
  value: string;
  label: MessageKey;
}

const executionOptions: readonly RoutingOption[] = [
  { value: "prefer_worker", label: "web.admin.settings.nodeRouting.execution.preferWorker" },
  { value: "prefer_transcode", label: "web.admin.settings.nodeRouting.execution.preferTranscode" },
  { value: "worker_only", label: "web.admin.settings.nodeRouting.execution.workerOnly" },
  { value: "prefer_api", label: "web.admin.settings.nodeRouting.option.preferApi" },
  { value: "api_only", label: "web.admin.settings.nodeRouting.option.apiOnly" },
];

const egressOptions: readonly RoutingOption[] = [
  { value: "prefer_proxy", label: "web.admin.settings.nodeRouting.egress.preferProxy" },
  { value: "proxy_only", label: "web.admin.settings.nodeRouting.egress.proxyOnly" },
  { value: "prefer_api", label: "web.admin.settings.nodeRouting.option.preferApi" },
  { value: "api_only", label: "web.admin.settings.nodeRouting.option.apiOnly" },
];

type ExecutionWorkload = "remux" | "video_transcode";

// A remux can fall back from a transcode node to any worker; a video
// transcode runs only on transcode nodes.
function executionPreview(t: Translate, value: string, workload: ExecutionWorkload) {
  const remux = workload === "remux";
  switch (value) {
    case "prefer_transcode":
      return t(
        remux
          ? "web.admin.settings.nodeRouting.preview.remux.preferTranscode"
          : "web.admin.settings.nodeRouting.preview.videoTranscode.preferTranscode",
      );
    case "worker_only":
      return t(
        remux
          ? "web.admin.settings.nodeRouting.preview.remux.workerOnly"
          : "web.admin.settings.nodeRouting.preview.videoTranscode.workerOnly",
      );
    case "prefer_api":
      return t(
        remux
          ? "web.admin.settings.nodeRouting.preview.remux.preferApi"
          : "web.admin.settings.nodeRouting.preview.videoTranscode.preferApi",
      );
    case "api_only":
      return t("web.admin.settings.nodeRouting.preview.apiOnly");
    default:
      return t(
        remux
          ? "web.admin.settings.nodeRouting.preview.remux.preferWorker"
          : "web.admin.settings.nodeRouting.preview.videoTranscode.preferTranscode",
      );
  }
}

function egressPreview(t: Translate, value: string) {
  switch (value) {
    case "proxy_only":
      return t("web.admin.settings.nodeRouting.preview.egress.proxyOnly");
    case "prefer_api":
      return t("web.admin.settings.nodeRouting.preview.egress.preferApi");
    case "api_only":
      return t("web.admin.settings.nodeRouting.preview.apiOnly");
    default:
      return t("web.admin.settings.nodeRouting.preview.egress.preferProxy");
  }
}

/** The whole path for a workload that needs an executor, e.g. "Worker → API · Proxy → API". */
function routePreview(
  t: Translate,
  execution: string,
  egress: string,
  workload: ExecutionWorkload,
) {
  return t("web.admin.settings.nodeRouting.preview.route", {
    execution: executionPreview(t, execution, workload),
    egress: egressPreview(t, egress),
  });
}

// A routing policy either picks who runs the work (execution) or who serves the
// bytes (egress). The kind decides the choices offered and the value the server
// applies while the row has never been set.
const ROUTING_KINDS = {
  execution: { options: executionOptions, serverDefault: "prefer_transcode" },
  egress: { options: egressOptions, serverDefault: "prefer_proxy" },
};

type RoutingKind = keyof typeof ROUTING_KINDS;

const ROUTING_KEYS = [
  "playback.routing.direct_play_egress",
  "playback.routing.remux_execution",
  "playback.routing.remux_egress",
  "playback.routing.video_transcode_execution",
  "playback.routing.video_transcode_egress",
] as const;

type RoutingKey = (typeof ROUTING_KEYS)[number];

const FORM_KEYS: string[] = [...ROUTING_KEYS];

interface RoutingField {
  key: RoutingKey;
  label: MessageKey;
  kind: RoutingKind;
  description?: MessageKey;
}

// One row per key in ROUTING_KEYS, in the order they render.
const ROUTING_FIELDS: readonly RoutingField[] = [
  {
    key: "playback.routing.direct_play_egress",
    label: "web.admin.settings.nodeRouting.fields.directPlayEgress.label",
    kind: "egress",
    description: "web.admin.settings.nodeRouting.fields.directPlayEgress.description",
  },
  {
    key: "playback.routing.remux_execution",
    label: "web.admin.settings.nodeRouting.fields.remuxExecution.label",
    kind: "execution",
    description: "web.admin.settings.nodeRouting.fields.remuxExecution.description",
  },
  {
    key: "playback.routing.remux_egress",
    label: "web.admin.settings.nodeRouting.fields.remuxEgress.label",
    kind: "egress",
  },
  {
    key: "playback.routing.video_transcode_execution",
    label: "web.admin.settings.nodeRouting.fields.videoTranscodeExecution.label",
    kind: "execution",
    description: "web.admin.settings.nodeRouting.fields.videoTranscodeExecution.description",
  },
  {
    key: "playback.routing.video_transcode_egress",
    label: "web.admin.settings.nodeRouting.fields.videoTranscodeEgress.label",
    kind: "egress",
  },
];

const ROUTING_PRESETS: Record<
  string,
  { label: MessageKey; values: Readonly<Record<RoutingKey, string>> }
> = {
  standard: {
    label: "web.admin.settings.nodeRouting.presets.standard",
    values: {
      "playback.routing.direct_play_egress": "prefer_proxy",
      "playback.routing.remux_execution": "prefer_transcode",
      "playback.routing.remux_egress": "prefer_proxy",
      "playback.routing.video_transcode_execution": "prefer_transcode",
      "playback.routing.video_transcode_egress": "prefer_proxy",
    },
  },
  gpu: {
    label: "web.admin.settings.nodeRouting.presets.gpu",
    values: {
      "playback.routing.direct_play_egress": "prefer_api",
      "playback.routing.remux_execution": "prefer_api",
      "playback.routing.remux_egress": "prefer_api",
      "playback.routing.video_transcode_execution": "prefer_worker",
      "playback.routing.video_transcode_egress": "prefer_proxy",
    },
  },
  central: {
    label: "web.admin.settings.nodeRouting.presets.central",
    values: {
      "playback.routing.direct_play_egress": "api_only",
      "playback.routing.remux_execution": "prefer_worker",
      "playback.routing.remux_egress": "api_only",
      "playback.routing.video_transcode_execution": "prefer_worker",
      "playback.routing.video_transcode_egress": "api_only",
    },
  },
};

/** One line of the preferred-path summary: workload on the left, route on the right. */
function PreferredPathRow({ label, route }: { label: string; route: string }) {
  return (
    <div className="flex justify-between gap-5">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium">{route}</span>
    </div>
  );
}

/**
 * Where playback work runs and who serves the bytes, across the API server and
 * its proxy and transcode nodes. It lives on the Nodes page because it only
 * means anything once nodes exist, and it saves through its own bar.
 */
export function NodeRoutingSettings() {
  const t = useT();
  const form = useSettingsForm({ keys: FORM_KEYS });
  const restartKeys = useRestartKeys();
  const nodes = useAdminNodes();
  // Gate on a node list we actually have: while it loads or after it failed,
  // read every kind as available rather than warn on a transient error.
  const transcodeNodeAvailable = !nodes.isSuccess || hasUsableTranscodeNode(nodes.data);
  const proxyNodeAvailable =
    !nodes.isSuccess ||
    (nodes.data ?? []).some((node) => node.type === "proxy" && node.enabled && node.healthy);

  const routingValues = Object.fromEntries(
    ROUTING_KEYS.map((key) => [key, form.getValue(key)]),
  ) as Record<RoutingKey, string>;
  const activeRoutingPreset = Object.values(ROUTING_PRESETS).find((preset) =>
    ROUTING_KEYS.every((key) => routingValues[key] === preset.values[key]),
  );
  const applyRoutingPreset = (values: Record<RoutingKey, string>) => {
    for (const key of ROUTING_KEYS) form.setValue(key, values[key]);
  };

  const routingDirty = ROUTING_KEYS.some((key) => form.isDirty(key));
  const usesProxyOnlyEgress = ROUTING_KEYS.some((key) => routingValues[key] === "proxy_only");
  // A remux can run on a proxy node (progressive) or a transcode node (HLS), so
  // worker-only remuxing is only stranded when neither kind is available.
  const strandedRoute =
    (usesProxyOnlyEgress && !proxyNodeAvailable) ||
    (routingValues["playback.routing.remux_execution"] === "worker_only" &&
      !proxyNodeAvailable &&
      !transcodeNodeAvailable) ||
    (routingValues["playback.routing.video_transcode_execution"] === "worker_only" &&
      !transcodeNodeAvailable);

  if (form.isLoading) return null;

  return (
    <>
      {/* The Nodes page is not under the settings layout, so it guards its
          own staged routing edits. */}
      <UnsavedChangesGuard />
      <FieldGroup
        label={t("web.admin.settings.nodeRouting.group")}
        description={t("web.admin.settings.nodeRouting.description")}
        restartAll={ROUTING_KEYS.every((key) => restartKeys.has(key))}
        dirty={routingDirty}
      >
        <SettingFieldRow
          label={t("web.admin.settings.nodeRouting.presets.label")}
          description={t("web.admin.settings.nodeRouting.presets.description")}
        >
          <div className="flex flex-wrap justify-end gap-2">
            {Object.entries(ROUTING_PRESETS).map(([id, preset]) => (
              <Button
                key={id}
                type="button"
                size="sm"
                variant={activeRoutingPreset === preset ? "default" : "outline"}
                onClick={() => applyRoutingPreset(preset.values)}
              >
                {t(preset.label)}
              </Button>
            ))}
            {!activeRoutingPreset && (
              <span className="text-muted-foreground self-center text-xs">
                {t("web.admin.settings.nodeRouting.presets.custom")}
              </span>
            )}
          </div>
        </SettingFieldRow>

        {/* The five routes are what a preset writes. An admin picks a preset;
            setting them one by one is the tuning tier. */}
        <AdvancedSection id="nodes.routing" count={ROUTING_FIELDS.length} forceOpen={routingDirty}>
          {ROUTING_FIELDS.map((field) => (
            <SettingField
              key={field.key}
              label={t(field.label)}
              type="select"
              options={ROUTING_KINDS[field.kind].options.map((option) => ({
                value: option.value,
                label: t(option.label),
              }))}
              description={field.description && t(field.description)}
              value={routingValues[field.key] || ROUTING_KINDS[field.kind].serverDefault}
              onChange={(v) => form.setValue(field.key, v)}
              restartRequired={restartKeys.has(field.key)}
            />
          ))}
        </AdvancedSection>

        <SettingFieldRow
          label={t("web.admin.settings.nodeRouting.paths.label")}
          description={t("web.admin.settings.nodeRouting.paths.description")}
        >
          <div className="grid min-w-64 gap-1 text-xs">
            <PreferredPathRow
              label={t("web.admin.settings.nodeRouting.paths.directPlay")}
              route={egressPreview(t, routingValues["playback.routing.direct_play_egress"])}
            />
            <PreferredPathRow
              label={t("web.admin.settings.nodeRouting.paths.remux")}
              route={routePreview(
                t,
                routingValues["playback.routing.remux_execution"],
                routingValues["playback.routing.remux_egress"],
                "remux",
              )}
            />
            <PreferredPathRow
              label={t("web.admin.settings.nodeRouting.paths.videoTranscode")}
              route={routePreview(
                t,
                routingValues["playback.routing.video_transcode_execution"],
                routingValues["playback.routing.video_transcode_egress"],
                "video_transcode",
              )}
            />
          </div>
        </SettingFieldRow>

        {strandedRoute && (
          <SettingFieldStatus tone="warn">
            {t("web.admin.settings.nodeRouting.warnings.stranded")}
          </SettingFieldStatus>
        )}
        {usesProxyOnlyEgress && (
          <SettingFieldStatus tone="warn">
            {t("web.admin.settings.nodeRouting.warnings.proxyOnlyClients")}
          </SettingFieldStatus>
        )}
      </FieldGroup>
      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </>
  );
}
