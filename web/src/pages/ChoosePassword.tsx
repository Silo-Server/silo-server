import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { Navigate, useSearchParams } from "react-router";
import { useChangeAccountPassword } from "@/hooks/queries/account";
import { useAuth } from "@/hooks/useAuth";
import { usePostSignInNavigation } from "@/hooks/usePostSignInNavigation";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { sanitizeAuthRedirect } from "@/lib/authRedirect";
import { newPasswordProblem, passwordErrorMessage } from "@/lib/newPassword";
import { Button } from "@/components/ui/button";
import { PasswordInput } from "@/components/PasswordInput";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { AuthBackground } from "@/components/auth/AuthBackground";

/**
 * The only screen a session holding a temporary password can use: the
 * account replaces the password an admin set, then continues signed in.
 */
export default function ChoosePassword() {
  const { user, pendingPasswordChange, loading, logout, settleTemporaryPassword } = useAuth();
  const [searchParams] = useSearchParams();
  const redirectTarget = sanitizeAuthRedirect(searchParams.get("redirect"));
  const continueSignIn = usePostSignInNavigation(redirectTarget);
  const changePassword = useChangeAccountPassword();
  const [current, setCurrent] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState("");
  // Set once the password changed, so the settled account does not trip the
  // redirect below before this screen navigates on.
  const [changed, setChanged] = useState(false);
  // The new password is saved but signing in with it did not finish; retrying
  // must not resubmit the temporary password, which no longer works.
  const [settleFailed, setSettleFailed] = useState(false);
  const busy = useRef(false);
  useDocumentTitle("Choose a New Password");

  if (loading) {
    return (
      <div className="auth-shell">
        <div className="border-primary h-8 w-8 animate-spin rounded-full border-b-2" />
      </div>
    );
  }
  // Stays mounted after the change settles the account, until it navigates on.
  const account = pendingPasswordChange ?? (changed ? user : null);
  if (!account) {
    return <Navigate to={user ? redirectTarget || "/" : "/login"} replace />;
  }

  async function settle() {
    busy.current = true;
    setSettleFailed(false);
    try {
      await settleTemporaryPassword();
      await continueSignIn({ password_change_required: false });
    } catch {
      setSettleFailed(true);
    } finally {
      busy.current = false;
    }
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (busy.current) return;
    const problem = newPasswordProblem(password, confirmation);
    if (problem) {
      setError(problem);
      return;
    }
    busy.current = true;
    setError("");
    try {
      await changePassword.mutateAsync({ current_password: current, new_password: password });
    } catch (err) {
      setError(passwordErrorMessage(err, "Could not change the password."));
      busy.current = false;
      return;
    }
    busy.current = false;
    setChanged(true);
    await settle();
  }

  if (settleFailed) {
    return (
      <div className="auth-shell">
        <AuthBackground />
        <Card className="auth-card glass panel-border w-full max-w-sm border-0">
          <CardHeader>
            <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
              Password changed
            </CardTitle>
            <CardDescription className="mt-2 text-sm leading-6">
              Your new password is saved, but signing you in did not finish.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <Button className="w-full" onClick={() => void settle()}>
              Try again
            </Button>
            <Button variant="outline" className="w-full" onClick={logout}>
              Sign out and use the new password
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  const submitting = changePassword.isPending || changed;
  return (
    <div className="auth-shell">
      <AuthBackground />
      <Card className="auth-card glass panel-border w-full max-w-sm border-0">
        <CardHeader>
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
            Choose a new password
          </CardTitle>
          <CardDescription className="mt-2 text-sm leading-6">
            An admin set a temporary password for {account.username}. Choose your own to continue.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {error && (
            <p role="alert" className="text-destructive mb-4 text-sm">
              {error}
            </p>
          )}
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="temporary-password">Temporary password</Label>
              <PasswordInput
                id="temporary-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                disabled={submitting}
                autoComplete="current-password"
                autoFocus
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="new-password">New password</Label>
              <p className="text-muted-foreground text-xs">At least 8 characters</p>
              <PasswordInput
                id="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                disabled={submitting}
                autoComplete="new-password"
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="confirm-new-password">Confirm new password</Label>
              <PasswordInput
                id="confirm-new-password"
                value={confirmation}
                onChange={(e) => setConfirmation(e.target.value)}
                disabled={submitting}
                autoComplete="new-password"
                required
              />
            </div>
            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting ? "Saving..." : "Save password"}
            </Button>
          </form>
          <p className="text-muted-foreground mt-4 text-center text-sm">
            Not you?{" "}
            <button
              type="button"
              className="text-foreground underline hover:no-underline"
              onClick={logout}
            >
              Sign out
            </button>
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
