import { useId, useState } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { copyTextToClipboard } from "@/lib/clipboard";

import { loadLocalWebhookBase, localWebhookURL, saveLocalWebhookBase } from "./webhookURL";

/**
 * The webhook URL as an arr on the same network reaches it.
 *
 * The server only knows its public URL, and inside a container it cannot see
 * the host's LAN address, so the base is the operator's to set. It starts as
 * the admin UI's own origin and is remembered per browser.
 *
 * `onCopy` routes copying through the caller's own guard, so a view bound to a
 * captured profile authority never copies the secret after that profile changed.
 */
export function LocalWebhookURL({
  url,
  onCopy,
}: {
  url: string;
  onCopy?: (text: string) => Promise<void>;
}) {
  const id = useId();
  const [base, setBase] = useState(loadLocalWebhookBase);
  const [copied, setCopied] = useState(false);
  const local = localWebhookURL(url, base);

  async function copy() {
    if (!local) return;
    if (onCopy) return onCopy(local);
    try {
      await copyTextToClipboard(local);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error("Couldn't copy — select the URL and copy it manually.");
    }
  }

  return (
    <div className="space-y-1.5">
      <Label htmlFor={`${id}-base`}>Local network address</Label>
      <Input
        id={`${id}-base`}
        value={base}
        placeholder="http://192.168.1.10:8080"
        className="font-mono text-xs"
        spellCheck={false}
        onChange={(e) => {
          setBase(e.target.value);
          saveLocalWebhookBase(e.target.value);
        }}
      />
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <Input
          readOnly
          value={local || "Enter Silo's address on your network, e.g. http://192.168.1.10:8080"}
          className="min-w-0 flex-1 font-mono text-xs"
          aria-label="Local webhook delivery URL"
          onFocus={(e) => e.currentTarget.select()}
        />
        <div className="flex shrink-0 gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={copy}
            disabled={!local}
            aria-label="Copy local webhook URL"
          >
            {copied ? <Check className="text-success" /> : <Copy />}
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
      </div>
      <p className="text-muted-foreground text-xs">
        Use this one when Sonarr/Radarr run on the same network as Silo, so deliveries skip the
        public address. Same secret — rotating changes both.
      </p>
    </div>
  );
}
