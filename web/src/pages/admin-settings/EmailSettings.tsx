import { TestEmailRow } from "@/components/admin/EmailTestRow";
import { SaveBar } from "@/components/SaveBar";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Skeleton } from "@/components/ui/skeleton";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT } from "@/i18n";

import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";

const EMAIL_KEYS = [
  "email.enabled",
  "email.from_address",
  "email.from_name",
  "email.smtp_host",
  "email.smtp_port",
  "email.smtp_security",
  "email.smtp_username",
  "email.smtp_password",
];

/** A protocol name; it reads the same in every language. */
const STARTTLS_LABEL = "STARTTLS";

/**
 * Outbound mail. It used to sit inside the Email channel card on Notifications,
 * but invitations and password resets send through the same server, so it has
 * its own page; the channel card only says whether mail is set up and links
 * here.
 */
export default function EmailSettings() {
  const t = useT();
  const form = useSettingsForm({ keys: EMAIL_KEYS });
  const restartKeys = useRestartKeys();

  const allRestart = EMAIL_KEYS.every((key) => restartKeys.has(key));
  const anyDirty = EMAIL_KEYS.some((key) => form.isDirty(key));

  if (form.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label={t("web.admin.settings.email.loading")}>
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">{t("web.admin.settings.email.loading")}</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.email.title")} />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label={t("web.admin.settings.email.mailServer")}
          description={t("web.admin.settings.email.mailServerDescription")}
          restartAll={allRestart}
          dirty={anyDirty}
        >
          <SettingField
            label={t("web.admin.settings.email.enabled")}
            dirty={form.isDirty("email.enabled")}
            type="toggle"
            value={form.getValue("email.enabled")}
            onChange={(v) => form.setValue("email.enabled", v)}
            restartRequired={restartKeys.has("email.enabled")}
          />
          <SettingField
            label={t("web.admin.settings.email.fromAddress")}
            dirty={form.isDirty("email.from_address")}
            // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
            hint="silo@example.com"
            value={form.getValue("email.from_address")}
            onChange={(v) => form.setValue("email.from_address", v)}
            restartRequired={restartKeys.has("email.from_address")}
          />
          <SettingField
            label={t("web.admin.settings.email.fromName")}
            dirty={form.isDirty("email.from_name")}
            // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
            hint="Silo"
            value={form.getValue("email.from_name")}
            onChange={(v) => form.setValue("email.from_name", v)}
            restartRequired={restartKeys.has("email.from_name")}
          />
          <SettingField
            label={t("web.admin.settings.email.host")}
            dirty={form.isDirty("email.smtp_host")}
            // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
            hint="smtp.example.com"
            value={form.getValue("email.smtp_host")}
            onChange={(v) => form.setValue("email.smtp_host", v)}
            restartRequired={restartKeys.has("email.smtp_host")}
          />
          <SettingField
            label={t("web.admin.settings.email.port")}
            dirty={form.isDirty("email.smtp_port")}
            description={t("web.admin.settings.email.portDescription")}
            type="number"
            value={form.getValue("email.smtp_port")}
            onChange={(v) => form.setValue("email.smtp_port", v)}
            restartRequired={restartKeys.has("email.smtp_port")}
          />
          <SettingField
            label={t("web.admin.settings.email.encryption")}
            dirty={form.isDirty("email.smtp_security")}
            description={t("web.admin.settings.email.encryptionDescription")}
            type="select"
            options={[
              { value: "starttls", label: STARTTLS_LABEL },
              { value: "tls", label: t("web.admin.settings.email.encryptionTls") },
              { value: "none", label: t("web.admin.settings.email.encryptionNone") },
            ]}
            value={form.getValue("email.smtp_security") || "starttls"}
            onChange={(v) => form.setValue("email.smtp_security", v)}
            restartRequired={restartKeys.has("email.smtp_security")}
          />
          <SettingField
            label={t("web.admin.settings.email.username")}
            dirty={form.isDirty("email.smtp_username")}
            description={t("web.admin.settings.email.usernameDescription")}
            value={form.getValue("email.smtp_username")}
            onChange={(v) => form.setValue("email.smtp_username", v)}
            restartRequired={restartKeys.has("email.smtp_username")}
          />
          <SecretField
            label={t("web.admin.settings.email.password")}
            value={form.getValue("email.smtp_password")}
            configured={form.sensitiveConfigured.includes("email.smtp_password")}
            onKeep={() => form.resetValue("email.smtp_password")}
            // The username above can be emptied for a relay that needs no
            // sign-in; without this the password could not follow it.
            onClear={() => form.setValue("email.smtp_password", "")}
            cleared={form.isClearStaged("email.smtp_password")}
            onChange={(v) => form.setValue("email.smtp_password", v)}
            restartRequired={restartKeys.has("email.smtp_password")}
          />
          <TestEmailRow />
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
