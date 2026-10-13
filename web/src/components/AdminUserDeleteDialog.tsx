import { useId, useRef, useState } from "react";
import { Info } from "lucide-react";
import { getAdminUser, type AdminUserEditor } from "@/api/v2/adminUsers";
import { V2ProblemError } from "@/api/v2/request";
import { useDeleteUser, useUpdateUser } from "@/hooks/queries/admin/users";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
} from "@/components/ui/alert-dialog";

/**
 * Deletes an account after the admin types its name. Where the caller
 * allows it, offers disabling instead, which keeps the account's history.
 */
export function AdminUserDeleteDialog({
  initialEditor,
  onClose,
  onDeleted,
  profileCount,
  onDisabled,
}: {
  initialEditor: AdminUserEditor;
  onClose: () => void;
  onDeleted: () => void;
  /** How many profiles go with the account, when known. */
  profileCount?: number;
  /** Offers "Disable instead"; called after the account is disabled. */
  onDisabled?: () => void;
}) {
  const [editor, setEditor] = useState(initialEditor);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [pending, setPending] = useState(false);
  const [typed, setTyped] = useState("");
  const busy = useRef(false);
  const deleteUser = useDeleteUser();
  const updateUser = useUpdateUser();
  const confirmId = useId();
  const username = editor.user.username;
  const confirmed = typed === username;
  const offerDisable = onDisabled !== undefined && editor.user.enabled;

  async function run(action: () => Promise<void>, fallback: string) {
    if (busy.current || conflict) return;
    busy.current = true;
    setPending(true);
    setError("");
    try {
      await action();
    } catch (err) {
      setError(err instanceof Error ? err.message : fallback);
      if (err instanceof V2ProblemError && err.status === 412) setConflict(true);
    } finally {
      busy.current = false;
      setPending(false);
    }
  }
  function remove() {
    if (!confirmed) return;
    void run(async () => {
      await deleteUser.mutateAsync(editor);
      onDeleted();
    }, "Could not delete user.");
  }
  function disable() {
    void run(async () => {
      await updateUser.mutateAsync({ editor, body: { enabled: false } });
      onDisabled?.();
    }, "Could not disable user.");
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
          <AlertDialogTitle>Delete {username}?</AlertDialogTitle>
          <AlertDialogDescription>
            This removes the account,{" "}
            {profileCount === undefined
              ? "its profiles"
              : `its ${profileCount} ${profileCount === 1 ? "profile" : "profiles"}`}
            , watch history, and saved preferences. It can't be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {offerDisable && (
          <div className="border-info/25 bg-info/5 text-muted-foreground flex items-start gap-2 rounded-xl border px-3 py-2.5 text-sm">
            <Info aria-hidden="true" className="text-info mt-0.5 size-4 shrink-0" />
            <span>
              Want to keep the history? <strong className="text-foreground">Disable</strong> the
              account instead. It stops sign-in and keeps everything.
            </span>
          </div>
        )}
        <div className="space-y-2">
          <label htmlFor={confirmId} className="text-sm font-medium">
            Type <span className="font-mono">{username}</span> to confirm
          </label>
          <Input
            id={confirmId}
            autoComplete="off"
            spellCheck={false}
            value={typed}
            onChange={(event) => setTyped(event.target.value)}
          />
        </div>
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
          <Button variant="ghost" disabled={pending} onClick={onClose}>
            Cancel
          </Button>
          {offerDisable && (
            <Button variant="outline" disabled={pending || conflict} onClick={disable}>
              Disable instead
            </Button>
          )}
          <Button
            variant="destructive"
            disabled={pending || conflict || !confirmed}
            onClick={remove}
          >
            Delete account
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
