import { useState } from "react";
import { toast } from "sonner";

import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import type { AutoscanSource } from "@/api/types";
import { autoscanSourceObservation } from "@/hooks/queries/admin/autoscanSourceObservation";
import {
  captureAutoscanWebhookIntent,
  type AutoscanWebhookIntent,
  useAutoscanWebhookUncertain,
  useCreateAutoscanWebhook,
  useRotateAutoscanWebhook,
} from "@/hooks/queries/useAutoscan";
import { copyTextToClipboard } from "@/lib/clipboard";

import { absoluteWebhookURL } from "./webhookURL";

/**
 * A webhook source's endpoint as one consumer sees it: the URL that is safe to
 * show, plus generate / rotate / copy actions bound to the profile authority
 * captured when the consumer mounted.
 *
 * The URL is withheld while a change is in flight or unconfirmed (the old
 * secret may already be dead) and when the active profile is no longer the one
 * that opened the view. The shown source is the newest observation of the
 * list read and this consumer's own mutation receipts, so an older read that
 * lands after a rotation cannot bring back the replaced secret.
 */
export function useWebhookEndpoint(source: AutoscanSource) {
  const [authority] = useState(captureProfileRequestContext);
  const createWebhook = useCreateAutoscanWebhook(authority);
  const rotateWebhook = useRotateAutoscanWebhook(authority);
  const [rotateTarget, setRotateTarget] = useState<AutoscanWebhookIntent | null>(null);
  const [action, setAction] = useState<"create" | "rotate">("create");

  const active = authority !== null && isCapturedProfileAuthorityActive(authority);
  const change = action === "rotate" ? rotateWebhook : createWebhook;
  // Uncertainty is shared with every other view of this source (row or edit
  // dialog), including this view's own failed change: it clears once the
  // source list has been read successfully after the failure, so this view
  // recovers too instead of staying locked on its mutation's error.
  const sharedUncertain = useAutoscanWebhookUncertain(source.id);
  const uncertain = change.isPending || sharedUncertain;

  const receipt = change.isSuccess && change.data?.id === source.id ? change.data : null;
  const candidate =
    autoscanSourceObservation(receipt) > autoscanSourceObservation(source) ? receipt! : source;
  const [latest, setLatest] = useState({ id: source.id, source: candidate });
  let view = latest.source;
  if (
    latest.id !== source.id ||
    autoscanSourceObservation(candidate) > autoscanSourceObservation(latest.source)
  ) {
    view = candidate;
    setLatest({ id: source.id, source: candidate });
  }

  const url = active && !uncertain && view?.webhook_url ? absoluteWebhookURL(view.webhook_url) : "";

  async function copy(failureHint = "select the URL manually") {
    if (!url || !authority || !isCapturedProfileAuthorityActive(authority)) return;
    try {
      await copyTextToClipboard(url);
      if (isCapturedProfileAuthorityActive(authority)) toast.success("Webhook URL copied");
    } catch {
      if (isCapturedProfileAuthorityActive(authority))
        toast.error(`Could not copy — ${failureHint}`);
    }
  }

  return {
    view,
    url,
    active,
    uncertain,
    generating: createWebhook.isPending,
    /** A failed generate whose outcome no source read has settled yet. */
    generateFailed: createWebhook.isError && sharedUncertain,
    rotating: rotateWebhook.isPending,
    copy,
    generate() {
      setAction("create");
      createWebhook.mutate(source.id);
    },
    /** Open the rotation confirmation, bound to this source and authority. */
    requestRotate() {
      if (authority && isCapturedProfileAuthorityActive(authority))
        setRotateTarget(captureAutoscanWebhookIntent(source.id, authority));
    },
    rotateTarget,
    cancelRotate: () => setRotateTarget(null),
    confirmRotate() {
      if (rotateTarget) {
        setAction("rotate");
        rotateWebhook.mutateCaptured(rotateTarget);
      }
      setRotateTarget(null);
    },
  };
}

export type WebhookEndpoint = ReturnType<typeof useWebhookEndpoint>;
