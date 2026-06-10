import { useId, useState } from "react";

import { useAdminSensitiveStatus, useUpdateServerSetting } from "@/hooks/queries/admin/settings";
import { useGenerateVapidKeys, usePushStatus, useSendTestPush } from "@/hooks/queries/push";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { CredentialStatus } from "./CredentialStatus";
import { SettingField } from "./SettingField";

interface CredentialFieldProps {
  settingKey: string;
  label: string;
  type?: "text" | "password";
  multiline?: boolean;
  hint?: string;
  value: string;
  onChange: (value: string) => void;
  sensitiveConfigured?: boolean;
}

/**
 * A single credential setting with its own Save button. SettingField has no
 * multiline variant, so we render a styled <textarea> for keys/JSON blobs.
 */
function CredentialField({
  settingKey,
  label,
  type = "text",
  multiline,
  hint,
  value,
  onChange,
  sensitiveConfigured,
}: CredentialFieldProps) {
  const updateSetting = useUpdateServerSetting();
  const controlId = useId();
  const hintId = useId();

  function save() {
    if (value.trim() === "") return;
    void updateSetting.mutateAsync({ key: settingKey, value }).then(() => {
      // Always clear the field on a successful save so it reverts to showing
      // the configured indicator/placeholder rather than the typed value.
      onChange("");
    });
  }

  return (
    <div className="space-y-2 py-2">
      {multiline ? (
        <div className="space-y-1">
          <Label htmlFor={controlId} className="text-sm font-medium">
            {label}
          </Label>
          <textarea
            id={controlId}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder={sensitiveConfigured ? "configured" : hint}
            aria-describedby={hint ? hintId : undefined}
            rows={5}
            className="border-input bg-background ring-offset-background focus-visible:ring-ring w-full max-w-md rounded-md border px-3 py-2 font-mono text-xs focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:outline-none"
          />
          {hint && (
            <p id={hintId} className="text-muted-foreground text-xs">
              {hint}
            </p>
          )}
        </div>
      ) : (
        <SettingField
          label={label}
          type={type}
          value={value}
          onChange={onChange}
          sensitiveConfigured={sensitiveConfigured}
          hint={hint}
        />
      )}
      <Button type="button" onClick={save} disabled={updateSetting.isPending}>
        {updateSetting.isPending ? "Saving..." : "Save"}
      </Button>
    </div>
  );
}

const SENSITIVE_HINT = "Leave blank to keep the current value.";

function WebPushCard() {
  const { data: pushStatus } = usePushStatus();
  const { data: sensitive } = useAdminSensitiveStatus();
  const generateKeys = useGenerateVapidKeys();
  const sendTest = useSendTestPush();

  const [vapidPublic, setVapidPublic] = useState("");
  const [vapidPrivate, setVapidPrivate] = useState("");
  const [subject, setSubject] = useState("");

  const configured = new Set(sensitive?.configured ?? []);
  const publicConfigured = configured.has("push.webpush.vapid_public");
  const privateConfigured = configured.has("push.webpush.vapid_private");
  const subjectConfigured = configured.has("push.webpush.subject");

  function generate() {
    void generateKeys.mutateAsync().then((keys) => {
      setVapidPublic(keys.vapid_public);
      setVapidPrivate(keys.vapid_private);
    });
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">Web Push</h3>
          <p className="text-muted-foreground text-xs">
            Browser push notifications via VAPID. Generate a key pair, review it, then save each
            field. Set a contact subject so push services can reach you.
          </p>
        </div>
        <CredentialStatus configured={pushStatus?.webpush ?? false} />
      </div>

      <CredentialField
        settingKey="push.webpush.vapid_public"
        label="VAPID Public Key"
        type="text"
        value={vapidPublic}
        onChange={setVapidPublic}
        sensitiveConfigured={publicConfigured}
      />
      <CredentialField
        settingKey="push.webpush.vapid_private"
        label="VAPID Private Key"
        type="password"
        value={vapidPrivate}
        onChange={setVapidPrivate}
        sensitiveConfigured={privateConfigured}
        hint={SENSITIVE_HINT}
      />
      <CredentialField
        settingKey="push.webpush.subject"
        label="Subject"
        type="text"
        value={subject}
        onChange={setSubject}
        sensitiveConfigured={subjectConfigured}
        hint="mailto:you@example.com"
      />

      <div className="mt-3 flex flex-wrap gap-2">
        <Button
          type="button"
          variant="outline"
          onClick={generate}
          disabled={generateKeys.isPending}
        >
          {generateKeys.isPending ? "Generating..." : "Generate keys"}
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={() => sendTest.mutate()}
          disabled={sendTest.isPending}
        >
          Send test push
        </Button>
      </div>
    </div>
  );
}

function ApnsCard() {
  const { data: pushStatus } = usePushStatus();
  const { data: sensitive } = useAdminSensitiveStatus();

  const [p8Key, setP8Key] = useState("");
  const [keyId, setKeyId] = useState("");
  const [teamId, setTeamId] = useState("");
  const [bundleId, setBundleId] = useState("");

  const configured = new Set(sensitive?.configured ?? []);
  const p8Configured = configured.has("push.apns.p8_key");
  const keyIdConfigured = configured.has("push.apns.key_id");
  const teamIdConfigured = configured.has("push.apns.team_id");
  const bundleIdConfigured = configured.has("push.apns.bundle_id");

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">APNs</h3>
          <p className="text-muted-foreground text-xs">
            Apple Push Notification service for iOS devices. Paste your .p8 key and identifiers from
            the Apple Developer portal.
          </p>
        </div>
        <CredentialStatus configured={pushStatus?.apns ?? false} />
      </div>

      <CredentialField
        settingKey="push.apns.p8_key"
        label="Auth Key (.p8)"
        multiline
        value={p8Key}
        onChange={setP8Key}
        sensitiveConfigured={p8Configured}
        hint={SENSITIVE_HINT}
      />
      <CredentialField
        settingKey="push.apns.key_id"
        label="Key ID"
        type="text"
        value={keyId}
        onChange={setKeyId}
        sensitiveConfigured={keyIdConfigured}
      />
      <CredentialField
        settingKey="push.apns.team_id"
        label="Team ID"
        type="text"
        value={teamId}
        onChange={setTeamId}
        sensitiveConfigured={teamIdConfigured}
      />
      <CredentialField
        settingKey="push.apns.bundle_id"
        label="Bundle ID"
        type="text"
        value={bundleId}
        onChange={setBundleId}
        sensitiveConfigured={bundleIdConfigured}
      />
    </div>
  );
}

function FcmCard() {
  const { data: pushStatus } = usePushStatus();
  const { data: sensitive } = useAdminSensitiveStatus();

  const [serviceAccountJson, setServiceAccountJson] = useState("");

  const jsonConfigured = new Set(sensitive?.configured ?? []).has("push.fcm.service_account_json");

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">FCM</h3>
          <p className="text-muted-foreground text-xs">
            Firebase Cloud Messaging for Android devices. Paste the service account JSON from your
            Firebase project.
          </p>
        </div>
        <CredentialStatus configured={pushStatus?.fcm ?? false} />
      </div>

      <CredentialField
        settingKey="push.fcm.service_account_json"
        label="Service Account JSON"
        multiline
        value={serviceAccountJson}
        onChange={setServiceAccountJson}
        sensitiveConfigured={jsonConfigured}
        hint={SENSITIVE_HINT}
      />
    </div>
  );
}

export default function PushSettings() {
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Push Notifications</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure push providers for web, iOS, and Android clients. Each provider is independent —
          set up only the ones you need.
        </p>
      </div>

      <div className="space-y-4">
        <WebPushCard />
        <ApnsCard />
        <FcmCard />
      </div>
    </div>
  );
}
