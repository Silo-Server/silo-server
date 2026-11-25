import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { createInvitationAppCode, type AppCode } from "@/api/v2/appCodes";
import type { InvitationLookup } from "@/api/v2/publicInvitations";
import { V2ProblemError } from "@/api/v2/request";
import { OpenSiloStep } from "@/components/appKeys/OpenSiloStep";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import { useT } from "@/i18n";

type FieldError = { field: "username" | "email"; message: string };

/**
 * "Join with the Silo app" on the invite page. An invitation with no address
 * asks for a username and an optional email; then the server reserves the
 * name and answers an app code, and the Open Silo step hands it to the app,
 * which creates the account with its keys and no password. Nothing exists
 * until the app redeems the code, so the person may come back to this tab
 * and get a new one.
 */
export function InviteAppJoin({
  token,
  invitation,
}: {
  token: string;
  invitation: InvitationLookup;
}) {
  const t = useT();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [fieldError, setFieldError] = useState<FieldError>();
  const [appCode, setAppCode] = useState<AppCode | null>(null);
  const [pending, setPending] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);
  const lifetime = useRef<AbortController | null>(null);
  // What the current code was issued for, sent again when it is renewed.
  const chosen = useRef<{ username: string; email: string } | undefined>(undefined);
  const usernameInput = useRef<HTMLInputElement>(null);
  const emailInput = useRef<HTMLInputElement>(null);
  const continueButton = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => controller.abort();
  }, []);
  // Once the form is usable again focus returns to it: the button that was
  // pressed may be gone, and the inputs were disabled. A field's error is
  // read with the field (aria-describedby), as other forms here do, so it
  // isn't also an alert; a refusal that belongs to no field is the alert, and
  // focus goes back to the form's first control without reading it twice.
  useEffect(() => {
    if (pending || appCode) return;
    if (fieldError?.field === "email") emailInput.current?.focus();
    else if (fieldError || refusal) (usernameInput.current ?? continueButton.current)?.focus();
  }, [fieldError, refusal, pending, appCode]);

  // A refusal that belongs to a field sends the person back to the form.
  // The server names the field: a taken name is `duplicate` at body.username
  // or body.email, whichever clashed.
  function fieldRefusal(error: V2ProblemError): FieldError | null {
    const at = error.problem.errors?.[0];
    if (!invitation.email_required || !at) return null;
    if (error.problemType === "conflict" && at.code === "duplicate") {
      return at.location === "body.email"
        ? { field: "email", message: t("web.auth.invite.emailTaken") }
        : { field: "username", message: t("web.auth.invite.usernameTaken") };
    }
    if (error.problemType !== "validation_failed") return null;
    if (at.location === "body.email") return { field: "email", message: invalidEmailMessage() };
    if (at.location === "body.username")
      return {
        field: "username",
        message:
          at.code === "required"
            ? t("web.auth.invite.usernameRequired")
            : t("web.auth.invite.usernameIsAddress"),
      };
    return null;
  }

  function refusalMessage(error: unknown): string {
    if (!(error instanceof V2ProblemError)) return t("web.auth.appCode.failed");
    switch (error.problemType) {
      case "provider_sign_in_required":
        return t("web.auth.invite.appProviderOnly", { server: invitation.server_name });
      case "not_found":
        return t("web.auth.invite.unavailable");
      case "rate_limited":
        return t("web.auth.forgotPassword.rateLimited");
      case "capability_unsupported":
        return t("web.auth.invite.profileUnavailable");
      case "conflict":
        // An emailed invitation has no field to put it on: its address,
        // the account's name, is taken since the invitation was sent.
        return t("web.auth.invite.emailTaken");
      default:
        return t("web.auth.appCode.failed");
    }
  }

  async function issue() {
    const controller = lifetime.current;
    if (!controller || controller.signal.aborted) return;
    setPending(true);
    setRefusal(null);
    try {
      const code = await createInvitationAppCode(token, controller.signal, chosen.current);
      if (!controller.signal.aborted) setAppCode(code);
    } catch (error: unknown) {
      if (controller.signal.aborted) return;
      const field = error instanceof V2ProblemError ? fieldRefusal(error) : null;
      if (field) {
        setAppCode(null);
        setFieldError(field);
      } else {
        setRefusal(refusalMessage(error));
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (pending) return;
    const name = username.trim();
    const address = email.trim();
    if (!name) {
      setFieldError({ field: "username", message: t("web.auth.invite.usernameRequired") });
      return;
    }
    if (address && !isValidEmail(address)) {
      setFieldError({ field: "email", message: invalidEmailMessage() });
      return;
    }
    chosen.current = { username: name, email: address };
    void issue();
  }

  const refusalAlert = refusal && (
    <p role="alert" className="mb-4">
      {refusal}
    </p>
  );

  if (appCode) {
    return (
      <>
        {refusalAlert}
        <OpenSiloStep appCode={appCode} onRenew={() => void issue()} renewing={pending} />
      </>
    );
  }

  if (!invitation.email_required) {
    // An emailed invitation's account takes its address: nothing to ask.
    return (
      <div className="space-y-3">
        {refusalAlert}
        <p className="text-muted-foreground text-sm">
          {t("web.auth.invite.appAccountEmail", { email: invitation.email })}
        </p>
        <Button
          ref={continueButton}
          type="button"
          className="w-full"
          onClick={() => void issue()}
          disabled={pending}
          autoFocus
        >
          {t("web.auth.invite.appContinue")}
        </Button>
      </div>
    );
  }

  const errorFor = (field: FieldError["field"]) =>
    fieldError?.field === field ? fieldError.message : "";
  return (
    <form onSubmit={handleSubmit} className="space-y-4" noValidate>
      {refusalAlert}
      <div className="space-y-2">
        <Label htmlFor="invite-app-username">{t("web.auth.username")}</Label>
        <Input
          ref={usernameInput}
          id="invite-app-username"
          value={username}
          onChange={(e) => {
            setUsername(e.target.value);
            if (fieldError?.field === "username") setFieldError(undefined);
          }}
          disabled={pending}
          autoComplete="username"
          maxLength={254}
          aria-invalid={errorFor("username") ? true : undefined}
          aria-describedby={errorFor("username") ? "invite-app-username-error" : undefined}
          required
          autoFocus
        />
        {errorFor("username") && (
          <p id="invite-app-username-error" className="text-destructive text-xs">
            {errorFor("username")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="invite-app-email">{t("web.auth.invite.emailOptional")}</Label>
        <p className="text-muted-foreground text-xs">{t("web.auth.invite.appEmailHint")}</p>
        <Input
          ref={emailInput}
          id="invite-app-email"
          type="email"
          value={email}
          onChange={(e) => {
            setEmail(e.target.value);
            if (fieldError?.field === "email") setFieldError(undefined);
          }}
          disabled={pending}
          autoComplete="email"
          maxLength={254}
          aria-invalid={errorFor("email") ? true : undefined}
          aria-describedby={errorFor("email") ? "invite-app-email-error" : undefined}
        />
        {errorFor("email") && (
          <p id="invite-app-email-error" className="text-destructive text-xs">
            {errorFor("email")}
          </p>
        )}
      </div>
      <Button type="submit" className="w-full" disabled={pending}>
        {t("web.auth.invite.appContinue")}
      </Button>
    </form>
  );
}
