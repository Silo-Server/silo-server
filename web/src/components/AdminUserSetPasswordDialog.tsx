import { useId, useState, type FormEvent } from "react";

import type { AdminUser } from "@/api/types";
import { PasswordInput } from "@/components/PasswordInput";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { V2ProblemError } from "@/api/v2/request";
import { useUpdateAdminUserSignIn } from "@/hooks/queries/admin/externalSignIn";
import { useT } from "@/i18n";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import { cn } from "@/lib/utils";
import { adminSignInErrorText } from "@/lib/externalSignInAdmin";
import { newPasswordProblem, passwordErrorMessage } from "@/lib/newPassword";

/**
 * Sets a Silo password on an account that has no password sign-in, such as
 * one connected to a sign-in provider. The server turns the account's
 * password sign-in back on when an administrator writes a password, which is
 * how an account whose provider is gone is recovered. A reset link needs
 * password sign-in already on, so this is the one password action such an
 * account has.
 */
export function AdminUserSetPasswordDialog({
  user,
  serverPasswordLoginOn = true,
  onClose,
  onDone,
}: {
  user: AdminUser;
  /**
   * Whether Silo passwords sign in on this server (auth.local_password_login).
   * While it is off only break-glass accounts sign in with a password.
   */
  serverPasswordLoginOn?: boolean;
  onClose: () => void;
  onDone?: (message: string) => void;
}) {
  const t = useT();
  const update = useUpdateAdminUserSignIn();
  const [password, setPassword] = useState("");
  const [temporary, setTemporary] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const passwordId = useId();
  const hintId = useId();
  const temporaryId = useId();
  // The account's own password sign-in comes back on, but the server switch
  // still refuses every account that isn't break-glass.
  const waitsForServer = !serverPasswordLoginOn && !user.break_glass;

  function submit(event: FormEvent) {
    event.preventDefault();
    const problem = newPasswordProblem(password, password);
    if (problem) {
      setError(problem);
      return;
    }
    setError(null);
    update.mutate(
      {
        userId: user.id,
        body: { password, ...(temporary ? { require_password_change: true } : {}) },
      },
      {
        onSuccess: () => {
          const username = user.username;
          onDone?.(
            waitsForServer
              ? temporary
                ? t("web.admin.users.setPasswordDialog.doneWaitingMustChange", { username })
                : t("web.admin.users.setPasswordDialog.doneWaiting", { username })
              : temporary
                ? t("web.admin.users.setPasswordDialog.doneMustChange", { username })
                : t("web.admin.users.setPasswordDialog.done", { username }),
          );
          onClose();
        },
        onError: (err) =>
          setError(
            // A validation problem names the refused rule in its field detail.
            err instanceof V2ProblemError && err.problem.errors?.length
              ? passwordErrorMessage(err, t("web.admin.users.setPasswordDialog.failed"))
              : adminSignInErrorText(err, t("web.admin.users.setPasswordDialog.failed")),
          ),
      },
    );
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !update.isPending) onClose();
      }}
    >
      <DialogContent className={cn("sm:max-w-md", TOUCH_TARGETS)}>
        <form onSubmit={submit} className="space-y-4" noValidate>
          <DialogHeader>
            <DialogTitle>{t("web.admin.users.setPasswordDialog.title")}</DialogTitle>
            <DialogDescription>
              {waitsForServer
                ? t("web.admin.users.setPasswordDialog.descriptionServerOff", {
                    username: user.username,
                  })
                : t("web.admin.users.setPasswordDialog.description", { username: user.username })}
            </DialogDescription>
          </DialogHeader>
          {error ? (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          ) : null}
          <div className="space-y-2">
            <Label htmlFor={passwordId}>
              {t("web.admin.users.setPasswordDialog.passwordLabel")}
            </Label>
            <PasswordInput
              id={passwordId}
              autoComplete="new-password"
              autoFocus
              value={password}
              aria-describedby={hintId}
              aria-invalid={error !== null}
              onChange={(event) => setPassword(event.target.value)}
            />
            <p id={hintId} className="text-muted-foreground text-xs">
              {t("web.admin.users.setPasswordDialog.hint", { username: user.username })}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Switch id={temporaryId} checked={temporary} onCheckedChange={setTemporary} />
            <Label htmlFor={temporaryId} className="text-sm font-normal">
              {t("web.admin.users.setPasswordDialog.requireChange")}
            </Label>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={update.isPending}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={update.isPending}>
              {update.isPending
                ? t("web.admin.users.setPasswordDialog.saving")
                : t("web.admin.users.setPasswordDialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
