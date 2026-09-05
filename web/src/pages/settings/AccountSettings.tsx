import { useState, type FormEvent } from "react";
import { toast } from "sonner";

import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useAccountPasswordCapability, useChangeAccountPassword } from "@/hooks/queries/account";

function passwordByteLength(value: string) {
  return new TextEncoder().encode(value).length;
}

export default function AccountSettings() {
  const capability = useAccountPasswordCapability();
  const changePassword = useChangeAccountPassword();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [formError, setFormError] = useState<string | null>(null);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError(null);

    const limits = capability.data;
    if (!limits?.change_password) {
      setFormError("Password changes are unavailable for this account.");
      return;
    }
    if (newPassword !== confirmPassword) {
      setFormError("New passwords do not match.");
      return;
    }
    if (Array.from(newPassword).length < limits.minimum_password_length) {
      setFormError(`New password must be at least ${limits.minimum_password_length} characters.`);
      return;
    }
    if (passwordByteLength(newPassword) > limits.maximum_password_bytes) {
      setFormError(`New password must be at most ${limits.maximum_password_bytes} bytes.`);
      return;
    }

    try {
      await changePassword.mutateAsync({
        current_password: currentPassword,
        new_password: newPassword,
      });
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      toast.success("Password changed");
    } catch (error) {
      setFormError(error instanceof Error ? error.message : "Failed to change password.");
    }
  }

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Account</h2>
        <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
          Manage the sign-in credential shared by every profile on this account.
        </p>
      </div>

      <SettingsGroup
        title="Password"
        description="Only the primary profile can change the shared account password. Your current password is required."
      >
        {capability.isLoading ? (
          <div className="max-w-md space-y-4" role="status" aria-label="Loading password settings">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
        ) : capability.isError ? (
          <p className="text-destructive text-sm">
            Password settings could not be loaded. Refresh the page to try again.
          </p>
        ) : !capability.data?.change_password ? (
          <div className="max-w-2xl space-y-1 text-sm">
            <p className="font-medium">Local password changes are unavailable.</p>
            <p className="text-muted-foreground">
              This account signs in through an external provider, or the active profile does not own
              the account credential.
            </p>
          </div>
        ) : (
          <form className="max-w-md space-y-4" onSubmit={(event) => void handleSubmit(event)}>
            <div className="space-y-2">
              <Label htmlFor="account-current-password">Current password</Label>
              <Input
                id="account-current-password"
                type="password"
                autoComplete="current-password"
                value={currentPassword}
                onChange={(event) => setCurrentPassword(event.target.value)}
                required
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="account-new-password">New password</Label>
              <Input
                id="account-new-password"
                type="password"
                autoComplete="new-password"
                value={newPassword}
                onChange={(event) => setNewPassword(event.target.value)}
                aria-describedby="account-password-requirements"
                required
              />
              <p id="account-password-requirements" className="text-muted-foreground text-xs">
                At least {capability.data.minimum_password_length} characters and no more than{" "}
                {capability.data.maximum_password_bytes} bytes.
              </p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="account-confirm-password">Confirm new password</Label>
              <Input
                id="account-confirm-password"
                type="password"
                autoComplete="new-password"
                value={confirmPassword}
                onChange={(event) => setConfirmPassword(event.target.value)}
                required
              />
            </div>

            {formError ? (
              <p className="text-destructive text-sm" role="alert">
                {formError}
              </p>
            ) : null}

            <Button type="submit" disabled={changePassword.isPending}>
              {changePassword.isPending ? "Changing…" : "Change password"}
            </Button>
          </form>
        )}
      </SettingsGroup>
    </div>
  );
}
