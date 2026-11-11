import { useState } from "react";
import type { FormEvent } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
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
import { newPasswordProblem } from "@/lib/newPassword";
import { toast } from "sonner";
import { useT } from "@/i18n";

export default function Signup() {
  const [searchParams] = useSearchParams();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [inviteCode, setInviteCode] = useState(searchParams.get("code") ?? "");
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

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!isValidEmail(email)) {
      toast.error(invalidEmailMessage());
      return;
    }
    const passwordProblem = newPasswordProblem(password, confirmPassword);
    if (passwordProblem) {
      toast.error(passwordProblem);
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
      toast.error(err instanceof Error ? err.message : t("web.auth.signup.failed"));
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
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="signup-username">{t("web.auth.username")}</Label>
              <Input
                id="signup-username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
                autoFocus
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="signup-email">{t("web.auth.email")}</Label>
              <Input
                id="signup-email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                autoComplete="email"
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="signup-password">{t("web.auth.password")}</Label>
              <p className="text-muted-foreground text-xs">
                {t("web.auth.passwordRules.minLength")}
              </p>
              <PasswordInput
                id="signup-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="signup-confirm-password">{t("web.auth.confirmPassword")}</Label>
              <PasswordInput
                id="signup-confirm-password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                autoComplete="new-password"
                required
              />
              {confirmPassword && password !== confirmPassword && (
                <p className="text-destructive text-xs">{t("web.auth.passwordRules.mismatch")}</p>
              )}
            </div>
            <div className="space-y-2">
              <Label htmlFor="signup-invite-code">{t("web.auth.signup.inviteCodeLabel")}</Label>
              <Input
                id="signup-invite-code"
                value={inviteCode}
                onChange={(e) => setInviteCode(e.target.value)}
                placeholder={t("web.auth.signup.inviteCodePlaceholder")}
                required
              />
            </div>
            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting
                ? t("web.auth.passwordRules.creatingAccount")
                : t("web.auth.createAccount")}
            </Button>
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
