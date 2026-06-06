import { useMemo } from "react";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";
import { Skeleton } from "@/components/ui/skeleton";

const KEYS = [
  "scanner.workers",
  "scanner.file_removal_grace",
  "matcher.workers",
  "matcher.batch_size",
  "metadata.cache_images",
];

export default function ScannerSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });

  if (form.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">Loading settings</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Scanner & Matcher</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure scanner performance and metadata matching. Startup and recurring scans are
          managed in Scheduled Tasks.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Scanner">
          <SettingField
            label="Scanner Workers"
            type="number"
            value={form.getValue("scanner.workers")}
            onChange={(v) => form.setValue("scanner.workers", v)}
          />
          <SettingField
            label="File Removal Grace"
            type="duration"
            hint="e.g. 24h"
            value={form.getValue("scanner.file_removal_grace")}
            onChange={(v) => form.setValue("scanner.file_removal_grace", v)}
          />
        </FieldGroup>

        <FieldGroup label="Matcher">
          <SettingField
            label="Matcher Workers"
            type="number"
            value={form.getValue("matcher.workers")}
            onChange={(v) => form.setValue("matcher.workers", v)}
          />
          <SettingField
            label="Matcher Batch Size"
            type="number"
            value={form.getValue("matcher.batch_size")}
            onChange={(v) => form.setValue("matcher.batch_size", v)}
          />
        </FieldGroup>

        <FieldGroup label="Metadata">
          <SettingField
            label="Cache Images to S3"
            type="toggle"
            hint="Download artwork from metadata providers and store resized variants in public asset S3 storage. Private bucket + presigned URLs is fully supported."
            value={form.getValue("metadata.cache_images")}
            onChange={(v) => form.setValue("metadata.cache_images", v)}
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
