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

export type SubtitleDeleteTarget = { subtitle: AdminStoredSubtitle; scope: string };
export default function AdminSubtitleDeleteDialog({
  target,
  onClose,
}: {
  target: SubtitleDeleteTarget | null;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [ready, setReady] = useState<{
    target: SubtitleDeleteTarget;
    deletion: AdminSubtitleDeletion;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
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
            setError(err instanceof Error ? err.message : "Unable to load subtitle.");
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
      toast.success("Subtitle metadata deleted");
      onClose();
    } catch (err) {
      if (generation.current === token && adminSubtitleListScope() === target.scope)
        setError(err instanceof Error ? err.message : "Unable to confirm deletion.");
    }
  }
  return (
    <AlertDialog
      open={target !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete subtitle?</AlertDialogTitle>
          <AlertDialogDescription>
            {active
              ? `Remove ${active.subtitle.language.toUpperCase()} subtitle “${active.subtitle.release_name || active.subtitle.id}”? Metadata is removed first; stored-file cleanup is best effort.`
              : "Loading the current subtitle before confirmation…"}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert">{error} Close and review the current subtitle before another attempt.</p>
        )}
        <AlertDialogFooter>
          <Button variant="outline" onClick={onClose}>
            Close
          </Button>
          <Button
            variant="destructive"
            disabled={!active || attempted || !!error}
            onClick={() => void confirm()}
          >
            Delete
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
