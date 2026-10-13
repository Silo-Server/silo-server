import { useState } from "react";
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
  const [copied, setCopied] = useState(false);
  const triggers = triggersFor(provider);

  async function copyURL() {
    try {
      await copyTextToClipboard(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error("Couldn't copy — select the URL and copy it manually.");
    }
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="webhook-url">1. Copy this URL</Label>
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
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
      </div>

      <div className="space-y-1.5">
        <Label>2. In your download manager, go to</Label>
        <p className="border-border bg-muted/30 rounded-md border px-3 py-2 font-mono text-xs">
          {settingsPathFor(provider)}
        </p>
        <p className="text-muted-foreground text-xs">
          Paste the URL into <span className="font-medium">Webhook URL</span> and leave the method
          as <span className="font-medium">POST</span>. No username or password is needed. For an
          existing connection, replace its saved URL with this one.
        </p>
      </div>

      <div className="space-y-2">
        <Label>3. Tick these triggers</Label>
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
                <span className="font-medium">{trigger.label}</span>
                {!trigger.required && <span className="text-muted-foreground"> (optional)</span>}
                <span className="text-muted-foreground block text-xs">{trigger.reason}</span>
              </span>
            </li>
          ))}
        </ul>
        <p className="text-muted-foreground text-xs">
          Leave every other trigger unchecked — Silo ignores them.
        </p>
      </div>

      <p className="text-muted-foreground text-xs">
        Save the connection in your download manager. You can use its
        <span className="font-medium"> Test </span> button — Silo accepts test payloads and will
        show the delivery on this source.
      </p>
    </div>
  );
}

/** "A, B, C and D" */
function joinAnd(items: string[]): string {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

/**
 * The same instructions as WebhookInstructions, as one sentence: for a source
 * that is already set up, where the reader only needs a reminder.
 */
export function WebhookTriggerHint({ provider }: { provider: AutoscanWebhookProvider | "auto" }) {
  const triggers = triggersFor(provider).map((trigger) => trigger.label);
  return (
    <p className="text-muted-foreground text-xs">
      Paste into {settingsPathFor(provider)}, method POST. Tick {joinAnd(triggers)}.
    </p>
  );
}

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
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>Sent by</Label>
      <Select value={value} onValueChange={(next) => onChange(next as AutoscanWebhookProvider)}>
        <SelectTrigger id={id} className="w-full sm:w-56">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="auto">Detect automatically</SelectItem>
          <SelectItem value="sonarr">Sonarr</SelectItem>
          <SelectItem value="radarr">Radarr</SelectItem>
        </SelectContent>
      </Select>
      <p className="text-muted-foreground text-xs">
        Detect automatically reads Sonarr or Radarr from each delivery.
      </p>
    </div>
  );
}
