import { useState } from "react";
import type { FormEvent } from "react";
import { toast } from "sonner";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
  const { user, setupInitialUser, setSummary } = useWizardContext();
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
      toast.error(err instanceof Error ? err.message : "Failed to create admin account");
    }
  }

  // An account already exists and the next step's data is still on its way
  // (a reload landed here): there is no form to show, only the wait.
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
          <Field id="setup-username" label="Username">
            <Input
              id="setup-username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus
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
