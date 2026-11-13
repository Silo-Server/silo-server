import { useState, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";

import type { AutoscanWebhookProvider } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useT, type Translate } from "@/i18n";
import { copyTextToClipboard } from "@/lib/clipboard";

import { settingsPathFor, triggersFor } from "./webhookSetup";

/**
 * The copy-the-URL-into-your-arr half of webhook setup.
 *
 * This exists because the previous flow created a webhook source and then left
 * the operator to work out, unaided, that they had to generate a URL, find the
 * right screen in Sonarr, and tick a specific set of boxes. Every one of those
 * is now stated on screen, and the trigger list is derived from what the host
 * actually parses rather than from memory.
 */
export function WebhookInstructions({
  url,
  provider,
}: {
  url: string;
  provider: AutoscanWebhookProvider | "auto";
}) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const triggers = triggersFor(provider);

  async function copyURL() {
    try {
      await copyTextToClipboard(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error(t("web.admin.autoscan.webhook.instructions.copyFailed"));
    }
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="webhook-url">{t("web.admin.autoscan.webhook.instructions.copyStep")}</Label>
        <div className="flex gap-2">
          <Input
            id="webhook-url"
            readOnly
            value={url}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
          <Button type="button" variant="outline" size="sm" onClick={copyURL}>
            {copied ? <Check className="text-success" /> : <Copy />}
            {copied
              ? t("web.admin.autoscan.webhook.instructions.copied")
              : t("web.admin.autoscan.webhook.copyButton")}
          </Button>
        </div>
      </div>

      <div className="space-y-1.5">
        <Label>{t("web.admin.autoscan.webhook.instructions.settingsStep")}</Label>
        <p className="border-border bg-muted/30 rounded-md border px-3 py-2 font-mono text-xs">
          {settingsPathFor(provider)}
        </p>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.autoscan.webhook.instructions.pasteURL", { b: medium })}
        </p>
      </div>

      <div className="space-y-2">
        <Label>{t("web.admin.autoscan.webhook.instructions.triggersStep")}</Label>
        <ul className="space-y-2">
          {triggers.map((trigger) => (
            <li key={trigger.label} className="flex items-start gap-2 text-sm">
              <span
                aria-hidden
                className="border-muted-foreground/50 mt-0.5 grid size-4 shrink-0 place-items-center rounded-[3px] border"
              >
                <Check className="size-3" />
              </span>
              <span className="min-w-0">
                {trigger.required ? (
                  <span className="font-medium">{trigger.label}</span>
                ) : (
                  t("web.admin.autoscan.webhook.instructions.optionalTrigger", {
                    trigger: trigger.label,
                    b: medium,
                    muted: (chunks) => <span className="text-muted-foreground">{chunks}</span>,
                  })
                )}
                <span className="text-muted-foreground block text-xs">{t(trigger.reason)}</span>
              </span>
            </li>
          ))}
        </ul>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.autoscan.webhook.instructions.otherTriggers")}
        </p>
      </div>

      <p className="text-muted-foreground text-xs">
        {t("web.admin.autoscan.webhook.instructions.testDelivery", { b: medium })}
      </p>
    </div>
  );
}

function medium(chunks: ReactNode[]) {
  return <span className="font-medium">{chunks}</span>;
}

/** "A, B, C and D" */
function joinAnd(t: Translate, items: string[]): string {
  if (items.length <= 1) return items.join("");
  return t("web.admin.autoscan.webhook.triggerHint.listAnd", {
    items: items.slice(0, -1).join(", "),
    last: items[items.length - 1]!,
  });
}

/**
 * The same instructions as WebhookInstructions, as one sentence: for a source
 * that is already set up, where the reader only needs a reminder.
 */
export function WebhookTriggerHint({ provider }: { provider: AutoscanWebhookProvider | "auto" }) {
  const t = useT();
  const triggers = triggersFor(provider).map((trigger) => trigger.label);
  return (
    <p className="text-muted-foreground text-xs">
      {t("web.admin.autoscan.webhook.triggerHint.text", {
        settings_path: settingsPathFor(provider),
        triggers: joinAnd(t, triggers),
      })}
    </p>
  );
}

const SONARR = "Sonarr";
const RADARR = "Radarr";

/** Which arr posts to a webhook source; drives the instructions and payload parsing. */
export function WebhookProviderSelect({
  id,
  value,
  onChange,
}: {
  id: string;
  value: AutoscanWebhookProvider;
  onChange: (next: AutoscanWebhookProvider) => void;
}) {
  const t = useT();
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{t("web.admin.autoscan.webhook.provider.label")}</Label>
      <Select value={value} onValueChange={(next) => onChange(next as AutoscanWebhookProvider)}>
        <SelectTrigger id={id} className="w-full sm:w-56">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="auto">{t("web.admin.autoscan.webhook.provider.auto")}</SelectItem>
          <SelectItem value="sonarr">{SONARR}</SelectItem>
          <SelectItem value="radarr">{RADARR}</SelectItem>
        </SelectContent>
      </Select>
      <p className="text-muted-foreground text-xs">
        {t("web.admin.autoscan.webhook.provider.autoHelp")}
      </p>
    </div>
  );
}
