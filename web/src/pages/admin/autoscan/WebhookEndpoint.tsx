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
import { useT } from "@/i18n";

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
  const t = useT();
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
            {sourceName
              ? t("web.admin.autoscan.webhook.rotate.titleNamed", { name: sourceName })
              : t("web.admin.autoscan.webhook.rotate.title")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("web.admin.autoscan.webhook.rotate.description")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction onClick={endpoint.confirmRotate}>
            {t("web.admin.autoscan.webhook.rotate.confirm")}
          </AlertDialogAction>
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
  const t = useT();
  const endpoint = useWebhookEndpoint(source);
  const { view, url, active, uncertain } = endpoint;

  return (
    <div className="space-y-1.5">
      <Label htmlFor={`webhook-url-${source.id}`}>
        {t("web.admin.autoscan.webhook.endpoint.label")}
      </Label>
      {view.webhook_configured ? (
        <>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Input
              id={`webhook-url-${source.id}`}
              readOnly
              value={
                active
                  ? uncertain
                    ? t("web.admin.autoscan.webhook.endpoint.reloadBeforeUse")
                    : url ||
                      t("web.admin.autoscan.webhook.endpoint.unavailable", {
                        suffix: view?.webhook_secret_suffix ?? "",
                      })
                  : t("web.admin.autoscan.webhook.endpoint.otherProfile")
              }
              className="min-w-0 flex-1 font-mono text-xs"
              aria-label={t("web.admin.autoscan.webhook.endpoint.fieldLabel")}
              onFocus={(e) => e.currentTarget.select()}
            />
            <div className="flex shrink-0 gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => endpoint.copy()}
                disabled={!url}
                aria-label={t("web.admin.autoscan.webhook.endpoint.copyLabel")}
              >
                <Copy />
                {t("web.admin.autoscan.webhook.copyButton")}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={endpoint.requestRotate}
                disabled={!active || uncertain}
                aria-label={t("web.admin.autoscan.webhook.endpoint.rotateLabel")}
                title={t("web.admin.autoscan.webhook.endpoint.rotateTitle")}
              >
                <RefreshCw className={endpoint.rotating ? "animate-spin" : undefined} />
                {t("web.admin.autoscan.webhook.endpoint.rotate")}
              </Button>
            </div>
          </div>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.autoscan.webhook.endpoint.alreadySetUp")}
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
              ? t("web.admin.autoscan.webhook.endpoint.generating")
              : endpoint.generateFailed
                ? t("web.admin.autoscan.webhook.endpoint.reloadToCheck")
                : t("web.admin.autoscan.webhook.endpoint.generate")}
          </Button>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.autoscan.webhook.endpoint.generateHelp")}
          </p>
        </div>
      )}
      <RotateWebhookDialog endpoint={endpoint} />
    </div>
  );
}
