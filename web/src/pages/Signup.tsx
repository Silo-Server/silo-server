import { useState } from "react";
import type { ChangeEvent, FormEvent } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { problemFieldDetail, v2, V2ProblemError } from "@/api/v2/request";
import { listProfiles } from "@/hooks/queries/profiles";
import { getBootstrapProfile, useAuth } from "@/hooks/useAuth";
import { Button } from "@/components/ui/button";
import { PasswordInput } from "@/components/PasswordInput";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { useServerBranding } from "@/hooks/useServerBranding";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { AuthLanguagePicker } from "@/components/auth/AuthLanguagePicker";
import { sanitizeAuthRedirect } from "@/lib/authRedirect";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import { newPasswordLengthProblem } from "@/lib/newPassword";
import { toast } from "sonner";
import { useT } from "@/i18n";

const FIELDS = ["username", "email", "password", "confirmPassword", "inviteCode"] as const;
type Field = (typeof FIELDS)[number];
type FieldMessages = Partial<Record<Field, string>>;

/** The form field each signup request member comes from, for placing a server refusal. */
const FIELD_BY_MEMBER: Record<string, Field> = {
  username: "username",
  email: "email",
  password: "password",
  invite_code: "inviteCode",
};

/** A refusal's field errors placed under their fields, plus the details no field takes. */
function placeServerErrors(err: unknown): { placed: FieldMessages; unplaced: string[] } | null {
  if (!(err instanceof V2ProblemError) || !err.problem.errors?.length) return null;
  const placed: FieldMessages = {};
  const unplaced: string[] = [];
  for (const error of err.problem.errors) {
    const member = error.location?.startsWith("body.") ? error.location.slice(5) : "";
    const field = FIELD_BY_MEMBER[member];
    if (field && !placed[field]) placed[field] = problemFieldDetail(error);
    else unplaced.push(problemFieldDetail(error));
  }
  return Object.keys(placed).length > 0 ? { placed, unplaced } : null;
}

function FieldError({ id, message }: { id: string; message?: string | null }) {
  if (!message) return null;
  return (
    <p id={id} className="text-destructive text-xs">
      {message}
    </p>
  );
}

export default function Signup() {
  const [searchParams] = useSearchParams();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [inviteCode, setInviteCode] = useState(searchParams.get("code") ?? "");
  // A field shows its rule once the user has typed in it or left it.
  const [touched, setTouched] = useState<Partial<Record<Field, boolean>>>({});
  const [serverErrors, setServerErrors] = useState<FieldMessages>({});
  const [submitting, setSubmitting] = useState(false);
  const { signup, profile, selectProfile, user, loading } = useAuth();
  const navigate = useNavigate();
  const { serverName } = useServerBranding();
  const redirectTarget = sanitizeAuthRedirect(searchParams.get("redirect"));
  const t = useT();

  const statusQuery = useQuery({
    queryKey: ["auth", "signup-status"],
    queryFn: () => v2("GET /api/v2/auth/signup"),
  });

  if (loading || statusQuery.isPending) {
    return (
      <div className="auth-shell">
        <div className="border-primary h-8 w-8 animate-spin rounded-full border-b-2" />
      </div>
    );
  }

  if (user) {
    return <Navigate to={redirectTarget || (profile ? "/" : "/profiles")} replace />;
  }

  if (statusQuery.data && !statusQuery.data.enabled) {
    return (
      <div className="auth-shell">
        <Card className="auth-card glass panel-border w-full max-w-sm border-0">
          <CardHeader>
            <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
              {serverName}
            </CardTitle>
            <CardDescription className="mt-2 text-sm leading-6">
              {t("web.auth.signup.closed")}
            </CardDescription>
          </CardHeader>
        </Card>
      </div>
    );
  }

  const required = (label: string) => t("web.common.schemaForm.required", { label });
  const problems: Record<Field, string | null> = {
    username: username.trim() ? null : required(t("web.auth.username")),
    email: isValidEmail(email) ? null : invalidEmailMessage(),
    password: newPasswordLengthProblem(password),
    confirmPassword: password === confirmPassword ? null : t("web.auth.passwordRules.mismatch"),
    inviteCode: inviteCode.trim() ? null : required(t("web.auth.signup.inviteCodeLabel")),
  };
  const invalid = FIELDS.some((field) => problems[field]);
  const errorFor = (field: Field) =>
    serverErrors[field] ?? (touched[field] ? problems[field] : null);

  function touch(field: Field) {
    setTouched((current) => (current[field] ? current : { ...current, [field]: true }));
  }

  function edit(field: Field, set: (value: string) => void) {
    return (e: ChangeEvent<HTMLInputElement>) => {
      set(e.target.value);
      touch(field);
      setServerErrors(({ [field]: _cleared, ...rest }) => rest);
    };
  }

  // The invalid state, error text, and blur marking every field shares.
  function fieldProps(field: Field, id: string) {
    const error = errorFor(field);
    return {
      id,
      onBlur: () => touch(field),
      "aria-invalid": error ? true : undefined,
      "aria-describedby": error ? `${id}-error` : undefined,
    };
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    // The disabled button is the usual guard; this one covers any other way the form submits.
    if (invalid) {
      setTouched(Object.fromEntries(FIELDS.map((field) => [field, true])));
      return;
    }
    setSubmitting(true);
    try {
      await signup(username, email, password, inviteCode);
      if (redirectTarget) {
        navigate(redirectTarget, { replace: true });
        return;
      }
      try {
        const profileList = await listProfiles();
        const soleProfile = getBootstrapProfile(profileList.profiles ?? []);
        if (soleProfile) {
          selectProfile(soleProfile);
          navigate("/");
          return;
        }
      } catch {
        navigate("/profiles");
        return;
      }
      navigate("/profiles");
    } catch (err) {
      const refusal = placeServerErrors(err);
      if (refusal) {
        setServerErrors(refusal.placed);
        if (refusal.unplaced.length > 0) toast.error(refusal.unplaced.join(" "));
      } else {
        toast.error(err instanceof Error ? err.message : t("web.auth.signup.failed"));
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="auth-shell">
      <AuthBackground />
      <AuthLanguagePicker />
      <Card className="auth-card glass panel-border w-full max-w-sm border-0">
        <CardHeader>
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">{serverName}</CardTitle>
          <CardDescription className="mt-2 text-sm leading-6">
            {t("web.auth.signup.description")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit}>
            {/* Locked while a request is out, so a refusal can't land on a value typed since. */}
            <fieldset disabled={submitting} className="min-w-0 space-y-4">
              <div className="space-y-2">
                <Label htmlFor="signup-username">{t("web.auth.username")}</Label>
                <Input
                  {...fieldProps("username", "signup-username")}
                  value={username}
                  onChange={edit("username", setUsername)}
                  autoComplete="username"
                  autoFocus
                  required
                />
                <FieldError id="signup-username-error" message={errorFor("username")} />
              </div>
              <div className="space-y-2">
                <Label htmlFor="signup-email">{t("web.auth.email")}</Label>
                <Input
                  {...fieldProps("email", "signup-email")}
                  type="email"
                  value={email}
                  onChange={edit("email", setEmail)}
                  autoComplete="email"
                  required
                />
                <FieldError id="signup-email-error" message={errorFor("email")} />
              </div>
              <div className="space-y-2">
                <Label htmlFor="signup-password">{t("web.auth.password")}</Label>
                <p className="text-muted-foreground text-xs">
                  {t("web.auth.passwordRules.minLength")}
                </p>
                <PasswordInput
                  {...fieldProps("password", "signup-password")}
                  value={password}
                  onChange={edit("password", setPassword)}
                  autoComplete="new-password"
                  required
                />
                <FieldError id="signup-password-error" message={errorFor("password")} />
              </div>
              <div className="space-y-2">
                <Label htmlFor="signup-confirm-password">{t("web.auth.confirmPassword")}</Label>
                <PasswordInput
                  {...fieldProps("confirmPassword", "signup-confirm-password")}
                  value={confirmPassword}
                  onChange={edit("confirmPassword", setConfirmPassword)}
                  autoComplete="new-password"
                  required
                />
                <FieldError
                  id="signup-confirm-password-error"
                  message={errorFor("confirmPassword")}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="signup-invite-code">{t("web.auth.signup.inviteCodeLabel")}</Label>
                <Input
                  {...fieldProps("inviteCode", "signup-invite-code")}
                  value={inviteCode}
                  onChange={edit("inviteCode", setInviteCode)}
                  placeholder={t("web.auth.signup.inviteCodePlaceholder")}
                  required
                />
                <FieldError id="signup-invite-code-error" message={errorFor("inviteCode")} />
              </div>
              <Button type="submit" className="w-full" disabled={submitting || invalid}>
                {submitting
                  ? t("web.auth.passwordRules.creatingAccount")
                  : t("web.auth.createAccount")}
              </Button>
            </fieldset>
          </form>
          <p className="text-muted-foreground mt-4 text-center text-sm">
            {t("web.auth.passwordRules.alreadyHaveAccount", {
              link: (chunks) => (
                <Link
                  to={
                    redirectTarget
                      ? `/login?redirect=${encodeURIComponent(redirectTarget)}`
                      : "/login"
                  }
                  className="text-foreground underline hover:no-underline"
                >
                  {chunks}
                </Link>
              ),
            })}
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
