import { useState, type FormEvent } from "react";
import { toast } from "sonner";

import { PageHeader } from "@/components/page/PageHeader";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useAccountPasswordCapability, useChangeAccountPassword } from "@/hooks/queries/account";
import { useT } from "@/i18n";
import { AccountSignInSection } from "./AccountSignInSection";

function passwordByteLength(value: string) {
  return new TextEncoder().encode(value).length;
}

export default function AccountSettings() {
  const t = useT();
  const capability = useAccountPasswordCapability();
  const changePassword = useChangeAccountPassword();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [signOutOtherSessions, setSignOutOtherSessions] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError(null);

    const limits = capability.data;
    if (!limits?.allowed) {
      setFormError(t("web.settings.account.password.error.unavailable"));
      return;
    }
    if (newPassword !== confirmPassword) {
      setFormError(t("web.settings.account.password.error.mismatch"));
      return;
    }
    if (Array.from(newPassword).length < limits.minimum_password_length) {
      setFormError(
        t("web.settings.account.password.error.tooShort", {
          minimum: limits.minimum_password_length,
          minimum_text: limits.minimum_password_length,
        }),
      );
      return;
    }
    if (passwordByteLength(newPassword) > limits.maximum_password_bytes) {
      setFormError(
        t("web.settings.account.password.error.tooLong", {
          maximum: limits.maximum_password_bytes,
        }),
      );
      return;
    }

    // Sent only when ticked and offered: a server without the option rejects
    // the member.
    const signOutOthers = signOutOtherSessions && limits.sign_out_other_sessions;
    try {
      await changePassword.mutateAsync({
        current_password: currentPassword,
        new_password: newPassword,
        ...(signOutOthers ? { sign_out_other_sessions: true } : {}),
      });
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      setSignOutOtherSessions(false);
      toast.success(
        signOutOthers
          ? t("web.settings.account.password.changedSignedOutOthers")
          : t("web.settings.account.password.changed"),
      );
    } catch (error) {
      setFormError(
        error instanceof Error ? error.message : t("web.settings.account.password.error.failed"),
      );
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.account.title")}
        subtitle={t("web.settings.account.subtitle")}
      />

      <SettingsGroup
        title={t("web.settings.account.password.title")}
        description={t("web.settings.account.password.description")}
      >
        {capability.isLoading ? (
          <div
            className="max-w-md space-y-4"
            role="status"
            aria-label={t("web.settings.account.password.loading")}
          >
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
        ) : capability.isError ? (
          <p className="text-destructive text-sm">
            {t("web.settings.account.password.loadFailed")}
          </p>
        ) : !capability.data?.allowed ? (
          <div className="max-w-2xl space-y-1 text-sm">
            <p className="font-medium">{t("web.settings.account.password.unavailableTitle")}</p>
            <p className="text-muted-foreground">
              {t("web.settings.account.password.unavailableBody")}
            </p>
          </div>
        ) : (
          <form className="max-w-md space-y-4" onSubmit={(event) => void handleSubmit(event)}>
            <div className="space-y-2">
              <Label htmlFor="account-current-password">
                {t("web.settings.account.password.current")}
              </Label>
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
              <Label htmlFor="account-new-password">{t("web.settings.account.password.new")}</Label>
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
                {t("web.settings.account.password.requirements", {
                  minimum: capability.data.minimum_password_length,
                  minimum_text: capability.data.minimum_password_length,
                  maximum: capability.data.maximum_password_bytes,
                })}
              </p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="account-confirm-password">
                {t("web.settings.account.password.confirm")}
              </Label>
              <Input
                id="account-confirm-password"
                type="password"
                autoComplete="new-password"
                value={confirmPassword}
                onChange={(event) => setConfirmPassword(event.target.value)}
                required
              />
            </div>

            {capability.data.sign_out_other_sessions ? (
              <div className="flex items-start gap-3">
                <Checkbox
                  id="account-sign-out-other-sessions"
                  className="mt-0.5"
                  checked={signOutOtherSessions}
                  onCheckedChange={(checked) => setSignOutOtherSessions(checked === true)}
                  aria-describedby="account-sign-out-other-sessions-description"
                />
                <div className="space-y-1">
                  <Label htmlFor="account-sign-out-other-sessions" className="font-normal">
                    {t("web.settings.account.password.signOutOthers")}
                  </Label>
                  <p
                    id="account-sign-out-other-sessions-description"
                    className="text-muted-foreground text-xs"
                  >
                    {t("web.settings.account.password.signOutOthersDescription")}
                  </p>
                </div>
              </div>
            ) : null}

            {formError ? (
              <p className="text-destructive text-sm" role="alert">
                {formError}
              </p>
            ) : null}

            <Button type="submit" disabled={changePassword.isPending}>
              {changePassword.isPending
                ? t("web.settings.account.password.changing")
                : t("web.settings.account.password.change")}
            </Button>
          </form>
        )}
      </SettingsGroup>

      <AccountSignInSection />
    </div>
  );
}
