import { useMemo } from "react";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";
import { Skeleton } from "@/components/ui/skeleton";

const JELLYFIN_KEYS = [
  "jellyfin_compat.public_url",
  "jellyfin_compat.server_name",
  "jellyfin_compat.server_id",
  "jellyfin_compat.emulated_server_version",
  "jellyfin_compat.session_ttl",
  "jellyfin_compat.playback_session_ttl",
];

const AUDIOBOOKSHELF_KEYS = ["audiobookshelf_compat.enabled"];

const KEYS = [...JELLYFIN_KEYS, ...AUDIOBOOKSHELF_KEYS];

export default function CompatibilityProxiesSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });

  if (form.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-56" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">Loading settings</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Compatibility Proxies</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure protocol-compatible listener surfaces for external client apps.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Jellyfin">
          <SettingField
            label="Public URL"
            value={form.getValue("jellyfin_compat.public_url")}
            onChange={(v) => form.setValue("jellyfin_compat.public_url", v)}
          />
          <SettingField
            label="Server Name"
            value={form.getValue("jellyfin_compat.server_name")}
            onChange={(v) => form.setValue("jellyfin_compat.server_name", v)}
          />
          <SettingField
            label="Server ID"
            value={form.getValue("jellyfin_compat.server_id")}
            onChange={(v) => form.setValue("jellyfin_compat.server_id", v)}
          />
          <SettingField
            label="Emulated Server Version"
            value={form.getValue("jellyfin_compat.emulated_server_version")}
            onChange={(v) => form.setValue("jellyfin_compat.emulated_server_version", v)}
          />
          <SettingField
            label="Session TTL"
            type="duration"
            hint="e.g. 24h"
            value={form.getValue("jellyfin_compat.session_ttl")}
            onChange={(v) => form.setValue("jellyfin_compat.session_ttl", v)}
          />
          <SettingField
            label="Playback Session TTL"
            type="duration"
            hint="e.g. 6h"
            value={form.getValue("jellyfin_compat.playback_session_ttl")}
            onChange={(v) => form.setValue("jellyfin_compat.playback_session_ttl", v)}
          />
        </FieldGroup>

        <FieldGroup label="Audiobookshelf">
          <SettingField
            label="Enable Audiobookshelf Proxy"
            type="toggle"
            hint="Starts the ABS-compatible API listener for external Audiobookshelf clients."
            value={form.getValue("audiobookshelf_compat.enabled")}
            onChange={(v) => form.setValue("audiobookshelf_compat.enabled", v)}
          />
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
