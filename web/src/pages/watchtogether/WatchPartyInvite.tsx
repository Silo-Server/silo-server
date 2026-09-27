import { Link, Navigate, useLocation, useSearchParams } from "react-router";
import { Smartphone } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { buildWatchPartyDeepLink, detectMobilePlatform } from "@/lib/appDeepLink";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";

/**
 * Landing page for shared Watch Party invitations (`/rooms/join?token=`).
 *
 * Silo runs on arbitrary domains, so the store apps can't claim these links
 * as universal links; on a phone they open in the browser. This page sits
 * outside the auth wrapper so a phone that isn't signed in to the web UI can
 * still hand the invitation to the app. It offers a user-tapped silo:// link
 * and never fires it automatically: there is no installed-app check, and a
 * miss shows an OS error. Everywhere else it forwards to the authenticated
 * hub, which auto-joins from the same query string.
 */
export default function WatchPartyInvite() {
  const [searchParams] = useSearchParams();
  const { search } = useLocation();
  const token = searchParams.get("token")?.trim() ?? "";
  const hubHref = `/rooms${search}`;
  const appLink =
    token && hasWatchPartyApp() ? buildWatchPartyDeepLink(window.location.origin, token) : null;

  if (!appLink) return <Navigate to={hubHref} replace />;
  return <AppHandoff appLink={appLink} hubHref={hubHref} />;
}

/**
 * Only the Apple app handles silo://watch-party so far. silo-android's intent
 * filter doesn't list the host yet, so a tap there would do nothing; add
 * "android" here once it does.
 */
function hasWatchPartyApp(): boolean {
  const ua = navigator.userAgent;
  // iPadOS Safari reports a Mac user agent; touch support tells it apart.
  const iPad = /Macintosh/.test(ua) && navigator.maxTouchPoints > 1;
  return detectMobilePlatform(ua) === "ios" || iPad;
}

function AppHandoff({ appLink, hubHref }: { appLink: string; hubHref: string }) {
  useDocumentTitle("Watch Party invitation");
  return (
    <div className="auth-shell">
      <AuthBackground />
      <Card className="auth-card glass panel-border w-full max-w-sm border-0">
        <CardHeader>
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
            Join the Watch Party
          </CardTitle>
          <CardDescription className="mt-2 text-sm leading-6">
            Open this invitation in the Silo app, or continue in the browser.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <Button asChild size="lg" className="h-12 w-full text-base font-semibold">
            <a href={appLink}>
              <Smartphone className="mr-2 h-5 w-5" /> Open in the Silo app
            </a>
          </Button>
          <p className="text-muted-foreground text-center text-xs">
            Nothing happens? The app isn&apos;t installed. Continue in the browser instead.
          </p>
          <Button asChild variant="outline" className="w-full">
            <Link to={hubHref} replace>
              Continue in the browser
            </Link>
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
