import { SaveBar } from "@/components/SaveBar";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { Button } from "@/components/ui/button";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";

import { FieldGroup } from "./FieldGroup";
import { SettingField, SettingFieldRow, SettingFieldStatus } from "./SettingField";
import { hasUsableTranscodeNode } from "./playbackSettings.utils";
// The settings layout loads these field styles for its pages; the Nodes page
// is not one of them.
import "@/styles/admin-settings.css";

const executionOptions = [
  { value: "prefer_worker", label: "Prefer any worker" },
  { value: "prefer_transcode", label: "Prefer transcode node" },
  { value: "worker_only", label: "Any worker only" },
  { value: "prefer_api", label: "Prefer API server" },
  { value: "api_only", label: "API server only" },
];

const egressOptions = [
  { value: "prefer_proxy", label: "Prefer proxy" },
  { value: "proxy_only", label: "Proxy only" },
  { value: "prefer_api", label: "Prefer API server" },
  { value: "api_only", label: "API server only" },
];

type ExecutionWorkload = "remux" | "video_transcode";

function executionPreview(value: string, workload: ExecutionWorkload) {
  const worker = workload === "remux" ? "Any worker" : "Transcode node";
  switch (value) {
    case "prefer_transcode":
      return workload === "remux" ? "Transcode node → any worker → API" : "Transcode node → API";
    case "worker_only":
      return `${worker} only`;
    case "prefer_api":
      return `API → ${worker.toLowerCase()}`;
    case "api_only":
      return "API only";
    default:
      return `${worker} → API`;
  }
}

function egressPreview(value: string) {
  switch (value) {
    case "proxy_only":
      return "Proxy only";
    case "prefer_api":
      return "API → proxy";
    case "api_only":
      return "API only";
    default:
      return "Proxy → API";
  }
}

/** The whole path for a workload that needs an executor, e.g. "Worker → API · Proxy → API". */
function routePreview(execution: string, egress: string, workload: ExecutionWorkload) {
  return `${executionPreview(execution, workload)} · ${egressPreview(egress)}`;
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
  label: string;
  kind: RoutingKind;
  description?: string;
}

// One row per key in ROUTING_KEYS, in the order they render.
const ROUTING_FIELDS: readonly RoutingField[] = [
  {
    key: "playback.routing.direct_play_egress",
    label: "Direct play egress",
    kind: "egress",
    description: "Original bytes need no executor.",
  },
  {
    key: "playback.routing.remux_execution",
    label: "Remux execution",
    kind: "execution",
    description:
      "A worker can be a proxy or transcode node. Prefer transcode node runs HLS or progressive remux there when supported; progressive output is relayed through the selected proxy.",
  },
  { key: "playback.routing.remux_egress", label: "Remux egress", kind: "egress" },
  {
    key: "playback.routing.video_transcode_execution",
    label: "Video transcode execution",
    kind: "execution",
    description: "Video transcode workers are transcode nodes; proxy nodes only provide egress.",
  },
  {
    key: "playback.routing.video_transcode_egress",
    label: "Video transcode egress",
    kind: "egress",
  },
];

const ROUTING_PRESETS = {
  standard: {
    label: "Silo Defaults",
    values: {
      "playback.routing.direct_play_egress": "prefer_proxy",
      "playback.routing.remux_execution": "prefer_transcode",
      "playback.routing.remux_egress": "prefer_proxy",
      "playback.routing.video_transcode_execution": "prefer_transcode",
      "playback.routing.video_transcode_egress": "prefer_proxy",
    },
  },
  gpu: {
    label: "GPU offload",
    values: {
      "playback.routing.direct_play_egress": "prefer_api",
      "playback.routing.remux_execution": "prefer_api",
      "playback.routing.remux_egress": "prefer_api",
      "playback.routing.video_transcode_execution": "prefer_worker",
      "playback.routing.video_transcode_egress": "prefer_proxy",
    },
  },
  central: {
    label: "Central egress",
    values: {
      "playback.routing.direct_play_egress": "api_only",
      "playback.routing.remux_execution": "prefer_worker",
      "playback.routing.remux_egress": "api_only",
      "playback.routing.video_transcode_execution": "prefer_worker",
      "playback.routing.video_transcode_egress": "api_only",
    },
  },
} as const;

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
        label="Where playback work runs"
        description="Which machine runs a remux or transcode, and which one sends the stream to the player. A preferred route falls back to the API server; an “only” route never does."
        restartAll={ROUTING_KEYS.every((key) => restartKeys.has(key))}
        dirty={routingDirty}
      >
        <SettingFieldRow
          label="Routing preset"
          description="Sets all five routes under Advanced at once; Custom is not stored."
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
                {preset.label}
              </Button>
            ))}
            {!activeRoutingPreset && (
              <span className="text-muted-foreground self-center text-xs">Custom</span>
            )}
          </div>
        </SettingFieldRow>

        {/* The five routes are what a preset writes. An admin picks a preset;
            setting them one by one is the tuning tier. */}
        <AdvancedSection id="nodes.routing" count={ROUTING_FIELDS.length} forceOpen={routingDirty}>
          {ROUTING_FIELDS.map((field) => (
            <SettingField
              key={field.key}
              label={field.label}
              type="select"
              options={ROUTING_KINDS[field.kind].options}
              description={field.description}
              value={routingValues[field.key] || ROUTING_KINDS[field.kind].serverDefault}
              onChange={(v) => form.setValue(field.key, v)}
              restartRequired={restartKeys.has(field.key)}
            />
          ))}
        </AdvancedSection>

        <SettingFieldRow
          label="Preferred paths"
          description="Arrows show soft fallback order; only modes never cross that boundary."
        >
          <div className="grid min-w-64 gap-1 text-xs">
            <PreferredPathRow
              label="Direct play"
              route={egressPreview(routingValues["playback.routing.direct_play_egress"])}
            />
            <PreferredPathRow
              label="Remux"
              route={routePreview(
                routingValues["playback.routing.remux_execution"],
                routingValues["playback.routing.remux_egress"],
                "remux",
              )}
            />
            <PreferredPathRow
              label="Video transcode"
              route={routePreview(
                routingValues["playback.routing.video_transcode_execution"],
                routingValues["playback.routing.video_transcode_egress"],
                "video_transcode",
              )}
            />
          </div>
        </SettingFieldRow>

        {strandedRoute && (
          <SettingFieldStatus tone="warn">
            An “only” route currently has no healthy supporting node. Saving is allowed so nodes can
            join later.
          </SettingFieldStatus>
        )}
        {usesProxyOnlyEgress && (
          <SettingFieldStatus tone="warn">
            Proxy-only egress requires every native client to support authorized media origins;
            older clients may be unable to start that workload.
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
