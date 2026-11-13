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
import { useT, type MessageKey } from "@/i18n";
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
  const t = useT();
  const timeId = useId();
  const keepId = useId();
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  return (
    <>
      <FieldGroup
        label={t("web.admin.backups.schedule.group")}
        description={t("web.admin.backups.schedule.groupDescription")}
        dirty={anyDirty(BACKUP_SCHEDULE_KEYS)}
      >
        <SettingField
          label={t("web.admin.backups.schedule.enabled")}
          type="toggle"
          dirty={form.isDirty(BACKUP_ENABLED_KEY)}
          description={t("web.admin.backups.schedule.enabledDescription")}
          value={form.getValue(BACKUP_ENABLED_KEY)}
          onChange={(value) => form.setValue(BACKUP_ENABLED_KEY, value)}
        />
        <SettingFieldRow
          label={t("web.admin.backups.schedule.time")}
          htmlFor={timeId}
          dirty={form.isDirty(BACKUP_TIME_KEY)}
          description={t("web.admin.backups.schedule.timeDescription")}
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
          label={t("web.admin.backups.schedule.keep")}
          htmlFor={keepId}
          dirty={form.isDirty(BACKUP_KEEP_KEY)}
          description={t("web.admin.backups.schedule.keepDescription")}
          unit={t("web.admin.backups.schedule.keepUnit")}
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
        label={t("web.admin.backups.content.group")}
        description={t("web.admin.backups.content.groupDescription")}
        dirty={anyDirty(BACKUP_CONTENT_KEYS)}
      >
        <SettingField
          label={t("web.admin.backups.content.intro")}
          type="toggle"
          dirty={form.isDirty(BACKUP_INTRO_KEY)}
          description={t("web.admin.backups.content.introDescription")}
          value={form.getValue(BACKUP_INTRO_KEY)}
          onChange={(value) => form.setValue(BACKUP_INTRO_KEY, value)}
        />
        <SettingField
          label={t("web.admin.backups.content.subtitles")}
          type="toggle"
          dirty={form.isDirty(BACKUP_SUBTITLES_KEY)}
          description={t("web.admin.backups.content.subtitlesDescription")}
          value={form.getValue(BACKUP_SUBTITLES_KEY)}
          onChange={(value) => form.setValue(BACKUP_SUBTITLES_KEY, value)}
        />
      </FieldGroup>
    </>
  );
}

const DEFAULT_LOCATION_COPY: Record<
  BackupLocation,
  { label: MessageKey; description: MessageKey }
> = {
  none: {
    label: "web.admin.backups.location.onThisServer",
    description: "web.admin.backups.location.noneDescription",
  },
  bucket: {
    label: "web.admin.backups.location.bucket",
    description: "web.admin.backups.location.bucketDescription",
  },
  local: {
    label: "web.admin.backups.location.onThisServer",
    description: "web.admin.backups.location.localDescription",
  },
};

function StorageSettingsLink() {
  const t = useT();
  return (
    <Button asChild variant="outline" size="sm" className={SETTINGS_BUTTON}>
      <Link to={adminSettingsHref("storage")}>
        {t("web.admin.backups.location.storageSettings")}
      </Link>
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
        <div className="flex items-center gap-2 ps-[30px] sm:ms-auto sm:shrink-0 sm:ps-0">
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
  const t = useT();
  const ownCopy = {
    label: t(DEFAULT_LOCATION_COPY[location].label),
    description: t(DEFAULT_LOCATION_COPY[location].description),
  };
  const confirmKey = onConfirmKey ? (
    <Button variant="outline" size="sm" className={SETTINGS_BUTTON} onClick={onConfirmKey}>
      {t("web.admin.backups.confirmKey.button")}
    </Button>
  ) : null;
  if (!s3Offered) {
    return (
      <FieldGroup label={t("web.admin.backups.location.group")}>
        <SettingFieldRow
          label={location === "none" ? t("web.admin.backups.location.nowhere") : ownCopy.label}
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
      <FieldGroup label={t("web.admin.backups.location.group")} dirty={dirty}>
        <RadioGroup
          value={destination}
          onValueChange={choose}
          aria-label={t("web.admin.backups.location.group")}
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
            label={t("web.admin.backups.location.s3")}
            description={t("web.admin.backups.location.s3Description")}
          >
            <div className="flex flex-wrap justify-end gap-2">
              {savedDestination === "s3" ? confirmKey : null}
              <Button variant="outline" size="sm" className={SETTINGS_BUTTON} onClick={onSetUpS3}>
                {s3Configured
                  ? t("web.admin.backups.location.change")
                  : t("web.admin.backups.location.setUp")}
              </Button>
            </div>
          </DestinationOption>
        </RadioGroup>
      </FieldGroup>
      <p className="text-muted-foreground px-1 text-[13px] leading-relaxed">
        {t("web.admin.backups.location.previousStays")}
      </p>
    </div>
  );
}

/** How a full restore reaches the backup the command names. */
export type FullRestoreSource = "folder" | "bucket" | "download";

/** How the command finds the backup, as the start of the hint under it. */
function commandHint(source: FullRestoreSource, encrypted: boolean): MessageKey {
  switch (source) {
    case "download":
      return "web.admin.backups.command.hintDownload";
    case "bucket":
      return encrypted
        ? "web.admin.backups.command.hintBucketEncrypted"
        : "web.admin.backups.command.hintBucket";
    case "folder":
      return "web.admin.backups.command.hintFolder";
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
  const t = useT();
  const copy = () =>
    copyTextToClipboard(command).then(
      () => toast.success(t("web.admin.backups.command.copied")),
      () => toast.error(t("web.admin.backups.command.copyFailed")),
    );
  return (
    <FieldGroup
      label={t("web.admin.backups.command.group")}
      description={t("web.admin.backups.command.groupDescription")}
    >
      <div className="space-y-2 py-3.5">
        <div className="bg-background/60 flex items-start gap-2 rounded-lg border px-3 py-2">
          <pre className="min-w-0 flex-1 overflow-x-auto py-1 font-mono text-xs leading-5">
            <code>{command}</code>
          </pre>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => void copy()}
            aria-label={t("web.admin.backups.command.copy")}
          >
            <Copy aria-hidden="true" />
          </Button>
        </div>
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          {t("web.admin.backups.command.runHelp", {
            hint: t(commandHint(source, encrypted)),
            code: (chunks) => <code className="font-mono text-xs">{chunks}</code>,
          })}
        </p>
      </div>
    </FieldGroup>
  );
}
