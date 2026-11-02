import { useId, type ReactNode } from "react";
import { Copy } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import type { AdminBackupDestination as BackupDestination } from "@/api/v2/adminBackups";
import type { useSettingsForm } from "@/hooks/useSettingsForm";
import { adminSettingsHref } from "@/lib/adminSettingsSearch";
import { copyTextToClipboard } from "@/lib/clipboard";
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

const DEFAULT_LOCATION_COPY: Record<BackupLocation, { label: string; description: string }> = {
  none: {
    label: "On this server",
    description:
      "This server has no private storage for backups: its artwork is in S3 and no private bucket is set up. Add a private bucket in Storage settings to turn backups on.",
  },
  bucket: {
    label: "In the private bucket",
    description:
      "In the private S3 bucket Silo also uses for avatars and diagnostic bundles, encrypted with your SECRET_KEY.",
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

/** One option of the destination choice, laid out like a settings row. */
function DestinationOption({
  value,
  label,
  description,
  disabled,
  children,
}: {
  value: BackupDestination;
  label: string;
  description: string;
  disabled?: boolean;
  children?: ReactNode;
}) {
  const id = useId();
  const descriptionId = useId();
  return (
    <div className="settings-field-row border-border/60 flex flex-col gap-3 border-b py-3.5 last:border-b-0 sm:flex-row sm:items-start sm:gap-6">
      <div className="flex min-w-0 flex-1 items-start gap-3 sm:max-w-[520px]">
        <RadioGroupItem
          id={id}
          value={value}
          disabled={disabled}
          aria-describedby={descriptionId}
          className="mt-px"
        />
        <div className="min-w-0">
          <Label htmlFor={id} className="text-sm font-medium">
            {label}
          </Label>
          <p id={descriptionId} className="text-muted-foreground mt-1 text-[13px] leading-relaxed">
            {description}
          </p>
        </div>
      </div>
      {children ? (
        <div className="flex items-center gap-2 pl-[30px] sm:ml-auto sm:shrink-0 sm:pl-0">
          {children}
        </div>
      ) : null}
    </div>
  );
}

export function BackupLocationGroup({
  location,
  s3Offered,
  destination,
  savedDestination,
  dirty,
  s3Configured,
  onDestinationChange,
  onSetUpS3,
  onConfirmKey,
}: {
  /** Where the server's own storage is; "none" when it has none. */
  location: BackupLocation;
  /** This server can keep backups in an S3 bucket. */
  s3Offered: boolean;
  /** The destination as edited on this page. */
  destination: BackupDestination;
  /** The destination new backups go to now. */
  savedDestination: BackupDestination;
  dirty: boolean;
  /** A bucket is saved, so the button says "Change" rather than "Set up". */
  s3Configured: boolean;
  /** Stages a destination for the page's save bar. */
  onDestinationChange: (destination: BackupDestination) => void;
  /** Opens the bucket setup, which saves on its own. */
  onSetUpS3: () => void;
  /**
   * Set while encrypted backups wait for the SECRET_KEY to be confirmed:
   * opens the confirmation, offered beside the destination in use.
   */
  onConfirmKey?: () => void;
}) {
  const ownCopy = DEFAULT_LOCATION_COPY[location];
  const confirmKey = onConfirmKey ? (
    <Button variant="outline" size="sm" className={SETTINGS_BUTTON} onClick={onConfirmKey}>
      Confirm the key
    </Button>
  ) : null;
  if (!s3Offered) {
    return (
      <FieldGroup label="Where backups are kept">
        <SettingFieldRow
          label={location === "none" ? "Nowhere yet" : ownCopy.label}
          description={ownCopy.description}
        >
          {location === "none" ? <StorageSettingsLink /> : confirmKey}
        </SettingFieldRow>
      </FieldGroup>
    );
  }
  const choose = (value: string) => {
    if (value === "s3" && savedDestination !== "s3") onSetUpS3();
    else onDestinationChange(value === "s3" ? "s3" : "default");
  };
  return (
    <div className="space-y-2">
      <FieldGroup label="Where backups are kept" dirty={dirty}>
        <RadioGroup
          value={destination}
          onValueChange={choose}
          aria-label="Where backups are kept"
          className="gap-0"
        >
          <DestinationOption
            value="default"
            label={ownCopy.label}
            description={ownCopy.description}
            disabled={location === "none"}
          >
            {location === "none" ? (
              <StorageSettingsLink />
            ) : savedDestination === "default" ? (
              confirmKey
            ) : null}
          </DestinationOption>
          <DestinationOption
            value="s3"
            label="In an S3 bucket"
            description="Off this server, encrypted with your SECRET_KEY."
          >
            <div className="flex flex-wrap justify-end gap-2">
              {savedDestination === "s3" ? confirmKey : null}
              <Button variant="outline" size="sm" className={SETTINGS_BUTTON} onClick={onSetUpS3}>
                {s3Configured ? "Change" : "Set up"}
              </Button>
            </div>
          </DestinationOption>
        </RadioGroup>
      </FieldGroup>
      <p className="text-muted-foreground px-1 text-[13px] leading-relaxed">
        Copies at the previous destination stay where they are. The list above shows only the
        current destination&apos;s backups.
      </p>
    </div>
  );
}

/** How a full restore reaches the backup the command names. */
export type FullRestoreSource = "folder" | "bucket" | "download";

const BUCKET_ENV =
  "SILO_BACKUP_S3_ENDPOINT, SILO_BACKUP_S3_REGION, SILO_BACKUP_S3_ACCESS_KEY, SILO_BACKUP_S3_SECRET_KEY, and SILO_BACKUP_S3_PATH_STYLE";

/** How the command finds the backup, as the start of the hint under it. */
function commandHint(source: FullRestoreSource, encrypted: boolean): string {
  switch (source) {
    case "download":
      return "Download the backup's files into one folder on the server and pass that folder. ";
    case "bucket":
      return `The command uses the backup selected above, or the newest one, and reads it from the bucket. Set ${encrypted ? "this server's SECRET_KEY and " : ""}${BUCKET_ENV} in its environment. `;
    case "folder":
      return "The command uses the backup selected above, or the newest one. ";
  }
}

export function RestoreCommandGroup({
  command,
  source,
  encrypted,
}: {
  command: string;
  /**
   * folder: the backup is on this server's disk; bucket: the command reads it
   * from its bucket; download: there is no backup to name yet.
   */
  source: FullRestoreSource;
  /** The backup is encrypted, so the command needs this server's SECRET_KEY. */
  encrypted: boolean;
}) {
  const copy = () =>
    copyTextToClipboard(command).then(
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
          {commandHint(source, encrypted)}
          Run <code className="font-mono text-xs">silo backup</code> without arguments for every
          option, including restoring into a separate database.
        </p>
      </div>
    </FieldGroup>
  );
}
