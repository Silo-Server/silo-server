import { useRef, useState } from "react";
import { getAdminUser, type AdminUserEditor } from "@/api/v2/adminUsers";
import { V2ProblemError } from "@/api/v2/request";
import { useDeleteUser } from "@/hooks/queries/admin/users";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
} from "@/components/ui/alert-dialog";

export function AdminUserDeleteDialog({
  initialEditor,
  onClose,
  onDeleted,
}: {
  initialEditor: AdminUserEditor;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const [editor, setEditor] = useState(initialEditor);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [pending, setPending] = useState(false);
  const busy = useRef(false);
  const mutation = useDeleteUser();
  async function remove() {
    if (busy.current || conflict) return;
    busy.current = true;
    setPending(true);
    setError("");
    try {
      await mutation.mutateAsync(editor);
      onDeleted();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not delete user.");
      if (err instanceof V2ProblemError && err.status === 412) setConflict(true);
    } finally {
      busy.current = false;
      setPending(false);
    }
  }
  async function reload() {
    if (busy.current) return;
    busy.current = true;
    setPending(true);
    try {
      setEditor(await getAdminUser(editor.user.id, editor.profileContext));
      setConflict(false);
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not reload user.");
    } finally {
      busy.current = false;
      setPending(false);
    }
  }
  return (
    <AlertDialog
      open
      onOpenChange={(open) => {
        if (!open && !busy.current) onClose();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete user</AlertDialogTitle>
          <AlertDialogDescription>
            Delete user “{editor.user.username}”? This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <p role="alert">{error}</p>}
        {conflict && (
          <>
            <p>The user changed. Reload before trying again.</p>
            <Button disabled={pending} onClick={() => void reload()}>
              Reload current user
            </Button>
          </>
        )}
        <AlertDialogFooter>
          <Button variant="outline" disabled={pending} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            disabled={pending || conflict}
            onClick={() => void remove()}
          >
            Delete
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
