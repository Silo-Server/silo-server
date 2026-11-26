import { useLocation, useNavigate } from "react-router";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/useAuth";
import { guardRedirectTarget } from "@/lib/authRedirect";
import { useT } from "@/i18n";

/**
 * Shown where this browser session can't manage the account's Silo app
 * devices because of how it was opened (by device sign-in, or before the
 * server recorded how sessions open). With `signInAgain` a fresh sign-in
 * here would work: the action ends this session, keeping the sign-in
 * provider's own, and returns to this page after signing in. Without it the
 * person has no other way into a browser, so the notice only points to the
 * app. Decide with `useAppKeySessionAccess`.
 */
export function SignInAgainNotice({ signInAgain }: { signInAgain: boolean }) {
  const t = useT();
  const { logoutOfSiloOnly } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  if (!signInAgain) {
    return (
      <p role="status" className="text-muted-foreground text-sm">
        {t("web.settings.signInAgain.useApp")}
      </p>
    );
  }
  return (
    <div role="status" className="space-y-3">
      <p className="text-sm">{t("web.settings.signInAgain.manageDevices")}</p>
      <Button
        type="button"
        variant="outline"
        onClick={() => {
          logoutOfSiloOnly();
          navigate(guardRedirectTarget("/login", location), { replace: true });
        }}
      >
        {t("web.settings.signInAgain.action")}
      </Button>
    </div>
  );
}
