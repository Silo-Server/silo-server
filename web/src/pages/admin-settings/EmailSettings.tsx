import { useMemo, useState } from "react";
import { Loader2, Send } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";

const KEYS = [
  "email.enabled",
  "email.smtp_host",
  "email.smtp_port",
  "email.smtp_security",
  "email.smtp_username",
  "email.smtp_password",
  "email.from_address",
  "email.from_name",
];

interface EmailTestResult {
  ok: boolean;
  duration_ms: number;
  message?: string;
}

function TestEmailRow() {
  const [recipient, setRecipient] = useState("");
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<EmailTestResult | null>(null);

  const sendTest = async () => {
    setPending(true);
    setResult(null);
    try {
      const response = await api<EmailTestResult>("/admin/email/test", {
        method: "POST",
        body: JSON.stringify({ to: recipient.trim() }),
      });
      setResult(response);
      if (response.ok) {
        toast.success("Test email sent");
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Test request failed");
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="space-y-2 py-2">
      <div className="flex gap-2">
        <Input
          type="email"
          placeholder="you@example.com"
          value={recipient}
          onChange={(event) => setRecipient(event.target.value)}
        />
        <Button
          variant="outline"
          disabled={pending || !recipient.trim()}
          onClick={() => void sendTest()}
        >
          {pending ? (
            <Loader2 className="mr-1.5 h-4 w-4 animate-spin" />
          ) : (
            <Send className="mr-1.5 h-4 w-4" />
          )}
          Send test
        </Button>
      </div>
      {result && (
        <p className={`text-xs ${result.ok ? "text-emerald-500" : "text-amber-500"}`}>
          {result.ok
            ? `Delivered to the SMTP server in ${result.duration_ms}ms.`
            : result.message || "Test failed."}
        </p>
      )}
      <p className="text-muted-foreground text-xs">
        Save your changes before testing — the test uses the stored settings.
      </p>
    </div>
  );
}

export default function EmailSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });

  if (form.isLoading) return <div>Loading...</div>;

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Email</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Outbound email via your own SMTP server. Used for notification-address verification and
          notification digests once those features are enabled.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="General">
          <SettingField
            label="Email Enabled"
            hint="Master switch for all outbound email"
            type="toggle"
            value={form.getValue("email.enabled")}
            onChange={(v) => form.setValue("email.enabled", v)}
          />
          <SettingField
            label="From Address"
            hint="The sender address, e.g. silo@example.com"
            value={form.getValue("email.from_address")}
            onChange={(v) => form.setValue("email.from_address", v)}
          />
          <SettingField
            label="From Name"
            hint='Display name on outgoing mail (default "Silo")'
            value={form.getValue("email.from_name")}
            onChange={(v) => form.setValue("email.from_name", v)}
          />
        </FieldGroup>

        <FieldGroup label="SMTP Server">
          <SettingField
            label="Host"
            hint="SMTP server hostname, e.g. smtp.example.com"
            value={form.getValue("email.smtp_host")}
            onChange={(v) => form.setValue("email.smtp_host", v)}
          />
          <SettingField
            label="Port"
            hint="587 for STARTTLS (typical), 465 for implicit TLS"
            type="number"
            value={form.getValue("email.smtp_port")}
            onChange={(v) => form.setValue("email.smtp_port", v)}
          />
          <SettingField
            label="Security"
            hint="STARTTLS upgrades a plain connection; TLS connects encrypted from the start"
            type="select"
            options={[
              { value: "starttls", label: "STARTTLS" },
              { value: "tls", label: "TLS (implicit)" },
              { value: "none", label: "None (insecure)" },
            ]}
            value={form.getValue("email.smtp_security") || "starttls"}
            onChange={(v) => form.setValue("email.smtp_security", v)}
          />
          <SettingField
            label="Username"
            hint="Leave empty when the server requires no authentication"
            value={form.getValue("email.smtp_username")}
            onChange={(v) => form.setValue("email.smtp_username", v)}
          />
          <SettingField
            label="Password"
            type="password"
            sensitiveConfigured={form.sensitiveConfigured.includes("email.smtp_password")}
            value={form.getValue("email.smtp_password")}
            onChange={(v) => form.setValue("email.smtp_password", v)}
          />
        </FieldGroup>

        <FieldGroup label="Verify">
          <TestEmailRow />
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
