import { useId, useRef, useState } from "react";
import { Info } from "lucide-react";
import { getAdminUser, type AdminUserEditor } from "@/api/v2/adminUsers";
import { V2ProblemError } from "@/api/v2/request";
import { useDeleteUser, useUpdateUser } from "@/hooks/queries/admin/users";
import { useT } from "@/i18n";
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
  const t = useT();
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
    }, t("web.admin.users.deleteDialog.deleteFailed"));
  }
  function disable() {
    void run(async () => {
      await updateUser.mutateAsync({ editor, body: { enabled: false } });
      onDisabled?.();
    }, t("web.admin.users.deleteDialog.disableFailed"));
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
      setError(err instanceof Error ? err.message : t("web.admin.users.deleteDialog.reloadFailed"));
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
          <AlertDialogTitle>
            {t("web.admin.users.deleteDialog.title", { username })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {profileCount === undefined
              ? t("web.admin.users.deleteDialog.description")
              : t("web.admin.users.deleteDialog.descriptionCounted", {
                  count: profileCount,
                  count_text: String(profileCount),
                })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {offerDisable && (
          <div className="border-info/25 bg-info/5 text-muted-foreground flex items-start gap-2 rounded-xl border px-3 py-2.5 text-sm">
            <Info aria-hidden="true" className="text-info mt-0.5 size-4 shrink-0" />
            <span>
              {t("web.admin.users.deleteDialog.disableHint", {
                b: (chunks) => <strong className="text-foreground">{chunks}</strong>,
              })}
            </span>
          </div>
        )}
        <div className="space-y-2">
          <label htmlFor={confirmId} className="text-sm font-medium">
            {t("web.admin.users.deleteDialog.confirmLabel", {
              username,
              code: (chunks) => <span className="font-mono">{chunks}</span>,
            })}
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
            <p>{t("web.admin.users.deleteDialog.conflict")}</p>
            <Button disabled={pending} onClick={() => void reload()}>
              {t("web.admin.users.deleteDialog.reload")}
            </Button>
          </>
        )}
        <AlertDialogFooter>
          <Button variant="ghost" disabled={pending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {offerDisable && (
            <Button variant="outline" disabled={pending || conflict} onClick={disable}>
              {t("web.admin.users.deleteDialog.disableInstead")}
            </Button>
          )}
          <Button
            variant="destructive"
            disabled={pending || conflict || !confirmed}
            onClick={remove}
          >
            {t("web.admin.users.deleteDialog.confirm")}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
