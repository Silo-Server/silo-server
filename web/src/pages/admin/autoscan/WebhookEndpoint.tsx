import { Copy, RefreshCw, Webhook } from "lucide-react";

import type { AutoscanSource } from "@/api/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

import { useWebhookEndpoint, type WebhookEndpoint } from "./useWebhookEndpoint";

/** Rotation is destructive for the connected service, so it always confirms. */
export function RotateWebhookDialog({
  endpoint,
  sourceName,
}: {
  endpoint: WebhookEndpoint;
  /** Named when the dialog is opened away from the source, e.g. from a list row. */
  sourceName?: string;
}) {
  return (
    <AlertDialog
      open={endpoint.rotateTarget !== null}
      onOpenChange={(open) => {
        if (!open) endpoint.cancelRotate();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {sourceName ? `Rotate the webhook URL for ${sourceName}?` : "Rotate webhook URL?"}
          </AlertDialogTitle>
          <AlertDialogDescription>
            The current URL stops working immediately. Sonarr/Radarr keep sending to the old URL
            until you paste the new one into their webhook settings.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={endpoint.confirmRotate}>Rotate</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/**
 * The webhook URL field with Copy and Rotate, or Generate when the source has
 * no endpoint yet. Used on the edit dialog's Connect tab.
 */
export function WebhookEndpointSection({ source }: { source: AutoscanSource }) {
  const endpoint = useWebhookEndpoint(source);
  const { view, url, active, uncertain } = endpoint;

  return (
    <div className="space-y-1.5">
      <Label htmlFor={`webhook-url-${source.id}`}>Webhook URL</Label>
      {view.webhook_configured ? (
        <>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Input
              id={`webhook-url-${source.id}`}
              readOnly
              value={
                active
                  ? uncertain
                    ? "Reload this page before using or replacing this URL"
                    : url || `…${view?.webhook_secret_suffix ?? ""} (URL unavailable)`
                  : "Select the original administrator profile to view this URL"
              }
              className="min-w-0 flex-1 font-mono text-xs"
              aria-label="Webhook delivery URL"
              onFocus={(e) => e.currentTarget.select()}
            />
            <div className="flex shrink-0 gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => endpoint.copy()}
                disabled={!url}
                aria-label="Copy webhook URL"
              >
                <Copy />
                Copy
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={endpoint.requestRotate}
                disabled={!active || uncertain}
                aria-label="Rotate webhook URL"
                title="Replace the URL — the old one stops working immediately"
              >
                <RefreshCw className={endpoint.rotating ? "animate-spin" : undefined} />
                Rotate
              </Button>
            </div>
          </div>
          <p className="text-muted-foreground text-xs">
            Already set up in your download manager? Nothing to change — the URL stays the same
            unless you rotate it.
          </p>
        </>
      ) : (
        <div className="space-y-1.5">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={!active || uncertain}
            onClick={endpoint.generate}
          >
            <Webhook />
            {endpoint.generating
              ? "Generating…"
              : endpoint.generateFailed
                ? "Reload this page to check the endpoint"
                : "Generate webhook URL"}
          </Button>
          <p className="text-muted-foreground text-xs">
            Creates the URL Sonarr/Radarr will POST import, rename, and delete events to.
          </p>
        </div>
      )}
      <RotateWebhookDialog endpoint={endpoint} />
    </div>
  );
}
