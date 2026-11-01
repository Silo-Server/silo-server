import { useId } from "react";
import { Copy } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { useSettingsForm } from "@/hooks/useSettingsForm";
import { adminSettingsHref } from "@/lib/adminSettingsSearch";
import { cn } from "@/lib/utils";
import { FieldGroup } from "@/pages/admin-settings/FieldGroup";
import {
  SETTINGS_BUTTON,
  SETTINGS_NUMBER_WIDTH,
  SettingField,
  SettingFieldRow,
} from "@/pages/admin-settings/SettingField";

import {
  BACKUP_CONTENT_KEYS,
  BACKUP_ENABLED_KEY,
  BACKUP_INTRO_KEY,
  BACKUP_KEEP_KEY,
  BACKUP_SCHEDULE_KEYS,
  BACKUP_SUBTITLES_KEY,
  BACKUP_TIME_KEY,
  type BackupLocation,
} from "./backupPresentation";

type SettingsForm = ReturnType<typeof useSettingsForm>;

export function BackupScheduleSettings({ form }: { form: SettingsForm }) {
  const timeId = useId();
  const keepId = useId();
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  return (
    <>
      <FieldGroup
        label="Schedule"
        description="One server in the cluster takes the backup. The oldest copy is deleted only after a new one succeeds."
        dirty={anyDirty(BACKUP_SCHEDULE_KEYS)}
      >
        <SettingField
          label="Nightly backups"
          type="toggle"
          dirty={form.isDirty(BACKUP_ENABLED_KEY)}
          description="Turning this off stops new backups. Saved copies stay."
          value={form.getValue(BACKUP_ENABLED_KEY)}
          onChange={(value) => form.setValue(BACKUP_ENABLED_KEY, value)}
        />
        <SettingFieldRow
          label="Start time"
          htmlFor={timeId}
          dirty={form.isDirty(BACKUP_TIME_KEY)}
          description="In the server's time zone."
        >
          <Input
            id={timeId}
            type="time"
            step={60}
            required
            value={form.getValue(BACKUP_TIME_KEY)}
            onChange={(event) => form.setValue(BACKUP_TIME_KEY, event.target.value)}
            className={cn("border-muted-foreground/25", SETTINGS_NUMBER_WIDTH, "sm:w-32")}
          />
        </SettingFieldRow>
        <SettingFieldRow
          label="Copies to keep"
          htmlFor={keepId}
          dirty={form.isDirty(BACKUP_KEEP_KEY)}
          description="Nightly and manual copies count toward this. From 1 to 60."
          unit="copies"
        >
          <Input
            id={keepId}
            type="number"
            min={1}
            max={60}
            step={1}
            value={form.getValue(BACKUP_KEEP_KEY)}
            onChange={(event) => form.setValue(BACKUP_KEEP_KEY, event.target.value)}
            className={cn("border-muted-foreground/25", SETTINGS_NUMBER_WIDTH)}
          />
        </SettingFieldRow>
      </FieldGroup>

      <FieldGroup
        label="What's included"
        description="The database and uploaded artwork, avatars, and branding are always included. Logs never are."
        dirty={anyDirty(BACKUP_CONTENT_KEYS)}
      >
        <SettingField
          label="Intro detection data"
          type="toggle"
          dirty={form.isDirty(BACKUP_INTRO_KEY)}
          description="Makes each copy larger. Without it, Silo analyzes a season again when its next episode arrives."
          value={form.getValue(BACKUP_INTRO_KEY)}
          onChange={(value) => form.setValue(BACKUP_INTRO_KEY, value)}
        />
        <SettingField
          label="Downloaded subtitles"
          type="toggle"
          dirty={form.isDirty(BACKUP_SUBTITLES_KEY)}
          description="Saves fetching them again. Subtitle sites limit downloads per day."
          value={form.getValue(BACKUP_SUBTITLES_KEY)}
          onChange={(value) => form.setValue(BACKUP_SUBTITLES_KEY, value)}
        />
      </FieldGroup>
    </>
  );
}

const LOCATION_COPY: Record<BackupLocation, { label: string; description: string }> = {
  none: {
    label: "Nowhere yet",
    description:
      "Backups are kept on this server's own disk, and this server keeps its artwork in S3. Switch to local storage in Storage settings to turn backups on.",
  },
  local: {
    label: "On this server",
    description:
      "In Silo's data folder, on the same disk as the database. Covers bad upgrades and mistakes, not a failed disk.",
  },
};

function StorageSettingsLink() {
  return (
    <Button asChild variant="outline" size="sm" className={SETTINGS_BUTTON}>
      <Link to={adminSettingsHref("storage")}>Open Storage settings</Link>
    </Button>
  );
}

export function BackupLocationGroup({
  location,
}: {
  /** Where the server's own storage is; "none" when it has none. */
  location: BackupLocation;
}) {
  const copy = LOCATION_COPY[location];
  return (
    <FieldGroup label="Where backups are kept">
      <SettingFieldRow label={copy.label} description={copy.description}>
        {location === "none" ? <StorageSettingsLink /> : null}
      </SettingFieldRow>
    </FieldGroup>
  );
}

/** How a full restore reaches the backup the command names. */
export type FullRestoreSource = "folder" | "download";

/** How the command finds the backup, as the start of the hint under it. */
function commandHint(source: FullRestoreSource): string {
  switch (source) {
    case "download":
      return "Download the backup's files into one folder on the server and pass that folder. ";
    case "folder":
      return "The command uses the newest backup. ";
  }
}

export function RestoreCommandGroup({
  command,
  source,
}: {
  command: string;
  /** folder: the backup is on this server's disk; download: there is no backup to name yet. */
  source: FullRestoreSource;
}) {
  const copy = () =>
    navigator.clipboard.writeText(command).then(
      () => toast.success("Command copied"),
      () => toast.error("Couldn't copy the command. Select it and copy it by hand."),
    );
  return (
    <FieldGroup
      label="Replace the whole server"
      description="Restoring everything replaces the database, so the server has to be stopped first."
    >
      <div className="space-y-2 py-3.5">
        <div className="bg-background/60 flex items-start gap-2 rounded-lg border px-3 py-2">
          <pre className="min-w-0 flex-1 overflow-x-auto py-1 font-mono text-xs leading-5">
            <code>{command}</code>
          </pre>
          <Button variant="ghost" size="icon-sm" onClick={() => void copy()} aria-label="Copy">
            <Copy aria-hidden="true" />
          </Button>
        </div>
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          {commandHint(source)}
          Run <code className="font-mono text-xs">silo backup</code> without arguments for every
          option, including restoring into a separate database.
        </p>
      </div>
    </FieldGroup>
  );
}
