import { useState } from "react";
import type { FormEvent } from "react";
import { Navigate } from "react-router";
import { toast } from "sonner";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { createProfile } from "@/hooks/queries/profiles";
import { useT } from "@/i18n";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";

import { StepFrame, StepSkeleton } from "../StepFrame";
import { useWizardContext } from "../WizardContext";

function Field({ id, label, children }: { id: string; label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id} className="text-[13px]">
        {label}
      </Label>
      {children}
    </div>
  );
}

export function AccountStep() {
  const {
    user,
    profile,
    profiles,
    profilesLoaded,
    profilesNeedPin,
    profilesError,
    retryProfiles,
    selectProfile,
    setupInitialUser,
    setSummary,
  } = useWizardContext();
  const t = useT();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [mismatch, setMismatch] = useState(false);
  const [emailInvalid, setEmailInvalid] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const badEmail = !isValidEmail(email);
    const badMatch = password !== confirmPassword;
    setEmailInvalid(badEmail);
    setMismatch(badMatch);
    if (badEmail || badMatch) return;

    setSubmitting(true);
    try {
      await setupInitialUser(username, email, password);
      setSummary("account", username);
      // Stay busy: this step unmounts as soon as the account exists.
    } catch (err) {
      setSubmitting(false);
      toast.error(err instanceof Error ? err.message : t("web.setup.account.createFailed"));
    }
  }

  // An account already exists and the next step's data is still on its way
  // (a reload landed here, or the profile lookup after creation failed):
  // there is no form to show, only the wait or a way to retry it.
  if (user && profilesError) {
    return (
      <StepFrame
        title={t("web.setup.account.profileLoadFailedTitle")}
        lede={t("web.setup.account.profileLoadFailedLede")}
        onContinue={retryProfiles}
        continueLabel={t("common.tryAgain")}
      >
        <div />
      </StepFrame>
    );
  }
  // The account exists but has no household profile to act as (created
  // through the API without one). Setup needs a profile for its settings
  // reads, so make the first one here.
  if (user && !profile && profilesLoaded && profiles.length === 0) {
    return <CreateProfileStep username={user.username} onCreated={selectProfile} />;
  }
  // Every profile is PIN-locked, so the wizard cannot act as one on its own.
  // The picker owns PIN entry; it brings the admin back here afterwards.
  if (user && !profile && profilesNeedPin) {
    return <Navigate to="/profiles?redirect=%2Fsetup" replace />;
  }
  if (user && !submitting) return <StepSkeleton rows={2} />;

  return (
    <StepFrame
      title={t("web.setup.account.title")}
      lede={t("web.setup.account.lede")}
      onSubmit={handleSubmit}
      continueLabel={t("web.auth.createAccount")}
      busyLabel={t("web.setup.account.creating")}
      busy={submitting}
    >
      <div className="setup-section setup-section-padded">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="setup-username" label={t("web.auth.username")}>
            <Input
              id="setup-username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus
              required
            />
          </Field>
          <Field id="setup-email" label={t("web.auth.email")}>
            <Input
              id="setup-email"
              type="email"
              value={email}
              onChange={(e) => {
                setEmail(e.target.value);
                if (emailInvalid) setEmailInvalid(false);
              }}
              autoComplete="email"
              aria-invalid={emailInvalid || undefined}
              aria-describedby={emailInvalid ? "setup-email-error" : undefined}
              required
            />
            {emailInvalid ? (
              <p id="setup-email-error" className="text-destructive text-xs">
                {invalidEmailMessage()}
              </p>
            ) : null}
          </Field>
          <Field id="setup-password" label={t("web.auth.password")}>
            <Input
              id="setup-password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
              required
            />
          </Field>
          <Field id="setup-confirm-password" label={t("web.auth.confirmPassword")}>
            <Input
              id="setup-confirm-password"
              type="password"
              value={confirmPassword}
              onChange={(e) => {
                setConfirmPassword(e.target.value);
                if (mismatch) setMismatch(false);
              }}
              autoComplete="new-password"
              aria-invalid={mismatch || undefined}
              aria-describedby={mismatch ? "setup-confirm-password-error" : undefined}
              required
            />
            {mismatch ? (
              <p id="setup-confirm-password-error" className="text-destructive text-xs">
                {t("web.setup.account.passwordMismatch")}
              </p>
            ) : null}
          </Field>
        </div>
      </div>
    </StepFrame>
  );
}

function CreateProfileStep({
  username,
  onCreated,
}: {
  username: string;
  onCreated: (profile: Awaited<ReturnType<typeof createProfile>>) => void;
}) {
  const t = useT();
  const [name, setName] = useState(username);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    try {
      onCreated(await createProfile({ name: name.trim() || username }));
    } catch (err) {
      setSubmitting(false);
      toast.error(err instanceof Error ? err.message : t("web.setup.account.createProfileFailed"));
    }
  }

  return (
    <StepFrame
      title={t("web.setup.account.profileTitle")}
      lede={t("web.setup.account.profileLede")}
      onSubmit={handleSubmit}
      continueLabel={t("web.setup.account.profileSubmit")}
      busyLabel={t("web.setup.account.creating")}
      busy={submitting}
    >
      <div className="setup-section setup-section-padded">
        <Field id="setup-profile-name" label={t("web.setup.account.profileName")}>
          <Input
            id="setup-profile-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
            required
          />
        </Field>
      </div>
    </StepFrame>
  );
}
