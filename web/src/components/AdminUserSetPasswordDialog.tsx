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
          const change = temporary ? " and must change it at the next sign-in" : "";
          onDone?.(
            waitsForServer
              ? `The password is set. ${user.username} can sign in with it${change} once password sign-in is on for the server.`
              : `${user.username} can sign in with the new password${change}.`,
          );
          onClose();
        },
        onError: (err) =>
          setError(
            // A validation problem names the refused rule in its field detail.
            err instanceof V2ProblemError && err.problem.errors?.length
              ? passwordErrorMessage(err, "Couldn't set the password.")
              : adminSignInErrorText(err, "Couldn't set the password."),
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
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="space-y-4" noValidate>
          <DialogHeader>
            <DialogTitle>Set a password</DialogTitle>
            <DialogDescription>
              {user.username} can't sign in with a Silo password now. Setting one turns password
              sign-in back on for this account. A connected sign-in provider keeps working.
              {waitsForServer
                ? " Password sign-in is off for the server, so the password works only once it's turned back on in Settings → Sign-in."
                : ""}
            </DialogDescription>
          </DialogHeader>
          {error ? (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          ) : null}
          <div className="space-y-2">
            <Label htmlFor={passwordId}>New password</Label>
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
              At least 8 characters, at most 72 bytes. Share it with {user.username} yourself.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Switch id={temporaryId} checked={temporary} onCheckedChange={setTemporary} />
            <Label htmlFor={temporaryId} className="text-sm font-normal">
              Require change at next sign-in
            </Label>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={update.isPending}>
              Cancel
            </Button>
            <Button type="submit" disabled={update.isPending}>
              {update.isPending ? "Setting..." : "Set password"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
