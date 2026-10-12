import { useState } from "react";
import type { FormEvent } from "react";
import { Navigate } from "react-router";
import { toast } from "sonner";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { V2ProblemError } from "@/api/v2/request";
import { createProfile } from "@/hooks/queries/profiles";
import { INVALID_EMAIL_MESSAGE, isValidEmail } from "@/lib/email";

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
    setupTokenRequired,
    setSummary,
  } = useWizardContext();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [mismatch, setMismatch] = useState(false);
  const [emailInvalid, setEmailInvalid] = useState(false);
  const [setupToken, setSetupToken] = useState("");
  // The server refused setup without a token (or with a wrong one) even if
  // the status read said none was needed, e.g. the network path changed.
  const [tokenRefused, setTokenRefused] = useState<"required" | "invalid" | null>(null);
  const showToken = setupTokenRequired || tokenRefused !== null;

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const badEmail = !isValidEmail(email);
    const badMatch = password !== confirmPassword;
    setEmailInvalid(badEmail);
    setMismatch(badMatch);
    if (badEmail || badMatch) return;

    setSubmitting(true);
    try {
      await setupInitialUser(username, email, password, showToken ? setupToken.trim() : undefined);
      setSummary("account", username);
      // Stay busy: this step unmounts as soon as the account exists.
    } catch (err) {
      setSubmitting(false);
      if (
        err instanceof V2ProblemError &&
        (err.problemType === "setup_token_required" || err.problemType === "setup_token_invalid")
      ) {
        setTokenRefused(err.problemType === "setup_token_invalid" ? "invalid" : "required");
        return;
      }
      toast.error(err instanceof Error ? err.message : "Failed to create admin account");
    }
  }

  // An account already exists and the next step's data is still on its way
  // (a reload landed here, or the profile lookup after creation failed):
  // there is no form to show, only the wait or a way to retry it.
  if (user && profilesError) {
    return (
      <StepFrame
        title="Couldn't load your profile"
        lede="The account was created, but the household profile it comes with could not be read. Try again to continue setup."
        onContinue={retryProfiles}
        continueLabel="Try again"
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
      title="Create the admin account"
      lede="This account runs the server. Household members get their own profiles on it later, and other people can be invited with their own accounts."
      onSubmit={handleSubmit}
      continueLabel="Create account"
      busyLabel="Creating…"
      busy={submitting}
    >
      <div className="setup-section setup-section-padded">
        <div className="grid gap-4 sm:grid-cols-2">
          {showToken ? (
            <div className="sm:col-span-2">
              <Field id="setup-token" label="Setup token">
                <Input
                  id="setup-token"
                  value={setupToken}
                  onChange={(e) => {
                    setSetupToken(e.target.value);
                    if (tokenRefused === "invalid") setTokenRefused("required");
                  }}
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus
                  aria-invalid={tokenRefused === "invalid" || undefined}
                  aria-describedby="setup-token-help"
                  required
                />
                <p
                  id="setup-token-help"
                  className={
                    tokenRefused === "invalid"
                      ? "text-destructive text-xs"
                      : "text-muted-foreground text-xs"
                  }
                >
                  {tokenRefused === "invalid"
                    ? "That token doesn't match. Copy it again from the server's console log; it changes every time the server starts."
                    : "Silo asks for this when the browser isn't on its local network or opened it by a domain name. Copy the setup token from the server's console log, where it is printed at startup."}
                </p>
              </Field>
            </div>
          ) : null}
          <Field id="setup-username" label="Username">
            <Input
              id="setup-username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus={!showToken}
              required
            />
          </Field>
          <Field id="setup-email" label="Email">
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
                {INVALID_EMAIL_MESSAGE}
              </p>
            ) : null}
          </Field>
          <Field id="setup-password" label="Password">
            <Input
              id="setup-password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
              required
            />
          </Field>
          <Field id="setup-confirm-password" label="Confirm password">
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
                The passwords don't match.
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
  const [name, setName] = useState(username);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    try {
      onCreated(await createProfile({ name: name.trim() || username }));
    } catch (err) {
      setSubmitting(false);
      toast.error(err instanceof Error ? err.message : "Failed to create profile");
    }
  }

  return (
    <StepFrame
      title="Create your profile"
      lede="Your account is ready. Profiles keep each household member's watch history and preferences apart; this first one is yours."
      onSubmit={handleSubmit}
      continueLabel="Create profile"
      busyLabel="Creating…"
      busy={submitting}
    >
      <div className="setup-section setup-section-padded">
        <Field id="setup-profile-name" label="Profile name">
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
