import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "@/api/v2/adminSubtitles";
import {
  prepareAdminSubtitleDeletion,
  type AdminSubtitleDeletion,
} from "@/api/v2/adminSubtitleDelete";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { useT, type MessageKey } from "@/i18n";

export type SubtitleDeleteTarget = { subtitle: AdminStoredSubtitle; scope: string };
export default function AdminSubtitleDeleteDialog({
  target,
  onClose,
}: {
  target: SubtitleDeleteTarget | null;
  onClose: () => void;
}) {
  const t = useT();
  const queryClient = useQueryClient();
  const [ready, setReady] = useState<{
    target: SubtitleDeleteTarget;
    deletion: AdminSubtitleDeletion;
  } | null>(null);
  // The failure, with the text to show when it carries none of its own.
  const [error, setError] = useState<{ cause: unknown; fallback: MessageKey } | null>(null);
  const [attempted, setAttempted] = useState(false);
  const generation = useRef(0);
  const [observedTarget, setObservedTarget] = useState(target);
  if (observedTarget !== target) {
    setObservedTarget(target);
    setReady(null);
    setError(null);
    setAttempted(false);
  }
  useEffect(() => {
    const token = ++generation.current;
    const abort = new AbortController();
    if (target)
      void prepareAdminSubtitleDeletion(target.subtitle, target.scope, abort.signal)
        .then((value) => {
          if (generation.current === token) setReady({ target, deletion: value });
        })
        .catch((err) => {
          if (generation.current === token)
            setError({ cause: err, fallback: "web.admin.common.subtitles.delete.loadFailed" });
        });
    return () => {
      generation.current = token + 1;
      abort.abort();
    };
  }, [target]);
  const active = ready?.target === target ? ready?.deletion : null;
  async function confirm() {
    if (!target || !active || attempted) return;
    const token = generation.current;
    setAttempted(true);
    try {
      await active.confirm();
      if (generation.current !== token || adminSubtitleListScope() !== target.scope) return;
      void queryClient.invalidateQueries({
        queryKey: ["admin", "downloadedSubtitles", target.scope],
      });
      toast.success(t("web.admin.common.subtitles.delete.deleted"));
      onClose();
    } catch (err) {
      if (generation.current === token && adminSubtitleListScope() === target.scope)
        setError({ cause: err, fallback: "web.admin.common.subtitles.delete.confirmFailed" });
    }
  }
  const errorText =
    error && (error.cause instanceof Error ? error.cause.message : t(error.fallback));
  return (
    <AlertDialog
      open={target !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("web.admin.common.subtitles.delete.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {active
              ? t("web.admin.common.subtitles.delete.description", {
                  language: active.subtitle.language.toUpperCase(),
                  name: active.subtitle.release_name || active.subtitle.id,
                })
              : t("web.admin.common.subtitles.delete.loading")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {errorText && (
          <p role="alert">
            {t("web.admin.common.subtitles.delete.errorHint", { error: errorText })}
          </p>
        )}
        <AlertDialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("web.admin.common.subtitles.delete.close")}
          </Button>
          <Button
            variant="destructive"
            disabled={!active || attempted || !!errorText}
            onClick={() => void confirm()}
          >
            {t("web.admin.common.subtitles.delete.confirm")}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
