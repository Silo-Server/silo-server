import { useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { Loader2, RefreshCw, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { useLoginSessions, type LoginSession } from "@/hooks/queries/loginSessions";
import { useAuth } from "@/hooks/useAuth";
import { loginSessionDevice } from "@/lib/loginSessionDevice";
import { useT } from "@/i18n";
import { LoginSessionRow } from "./LoginSessionRow";

type Selection = { kind: "one"; session: LoginSession } | { kind: "all" };

export function LoginSessionsPanel({
  adminUser,
  manageable = true,
}: {
  adminUser?: { id: number; username: string };
  manageable?: boolean;
}) {
  const t = useT();
  const { query, sessions, currentSession, revoke, revokeAll } = useLoginSessions(adminUser?.id);
  const { clearLoginSession } = useAuth();
  const navigate = useNavigate();
  const [selection, setSelection] = useState<Selection | null>(null);
  const [actionError, setActionError] = useState("");
  const pending = revoke.isPending || revokeAll.isPending;
  const heading = useRef<HTMLHeadingElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  const signedOut = useRef(false);
  const current = currentSession ?? sessions.find((row) => row.current) ?? null;
  const others = sessions.filter((row) => !row.current);
  const adminRows =
    current && !sessions.some((row) => row.id === current.id) ? [current, ...sessions] : sessions;

  function open(next: Selection) {
    returnFocus.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    signedOut.current = false;
    setActionError("");
    setSelection(next);
  }

  async function confirm() {
    if (!selection || pending) return;
    setActionError("");
    const endsThisBrowser = selection.kind === "all" ? Boolean(current) : selection.session.current;
    try {
      if (selection.kind === "all") await revokeAll.mutateAsync();
      else await revoke.mutateAsync(selection.session.id);
      signedOut.current = true;
      setSelection(null);
      if (endsThisBrowser) {
        clearLoginSession();
        navigate("/login", { replace: true });
      } else {
        toast.success(
          selection.kind === "all"
            ? t("web.settings.sessions.signedOutEverywhere", {
                username: adminUser?.username ?? "",
              })
            : t("web.settings.sessions.signedOut"),
        );
      }
    } catch (error) {
      setActionError(
        error instanceof Error ? error.message : t("web.settings.sessions.signOutFailed"),
      );
    }
  }

  const signOut = manageable
    ? (session: LoginSession) => open({ kind: "one", session })
    : undefined;
  const list = (rows: LoginSession[]) => (
    <ul className="mt-3 space-y-2">
      {rows.map((session) => (
        <LoginSessionRow key={session.id} session={session} onSignOut={signOut} />
      ))}
    </ul>
  );
  // A section of the admin's user page there; the page's own title in Settings.
  const Heading = adminUser ? "h2" : "h1";
  return (
    <div className="space-y-6">
      {/* In Settings this is the page header, so it matches `PageHeader`'s
          title and gap; it keeps its own markup for the focusable heading. */}
      <div
        className={cn(
          "flex flex-wrap items-start justify-between gap-3",
          !adminUser && "page-header mb-8",
        )}
      >
        <div className="space-y-2">
          <Heading
            ref={heading}
            tabIndex={-1}
            className={cn(
              "focus:outline-none",
              adminUser
                ? "text-lg font-semibold tracking-tight"
                : "text-[1.75rem] leading-[1.15] font-bold tracking-[-0.035em] text-balance sm:text-[2rem]",
            )}
          >
            {t("web.settings.sessions.title")}
          </Heading>
          <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
            {adminUser
              ? t("web.settings.sessions.adminDescription", { username: adminUser.username })
              : t("web.settings.sessions.description")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
            aria-label={t("web.settings.sessions.refreshLabel")}
          >
            <RefreshCw
              className={query.isFetching ? "animate-spin" : undefined}
              aria-hidden="true"
            />
            {t("web.settings.sessions.refresh")}
          </Button>
          {adminUser && manageable && !query.isPending && !query.isError && (
            <Button
              variant="outline"
              size="sm"
              // Revoking everything also ends Audiobookshelf sessions and approved,
              // uncollected device sign-ins, which this list does not show.
              disabled={pending}
              onClick={() => open({ kind: "all" })}
            >
              {t("web.settings.sessions.signOutEverywhere")}
            </Button>
          )}
        </div>
      </div>
      {query.isPending ? (
        <p role="status" className="text-muted-foreground flex items-center gap-2 text-sm">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          {t("web.settings.sessions.loading")}
        </p>
      ) : query.isError ? (
        <div role="alert" className="surface-panel-raised space-y-3 p-5">
          <p>{t("web.settings.sessions.loadFailed")}</p>
          <Button
            variant="outline"
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
          >
            {t("common.retry")}
          </Button>
        </div>
      ) : adminUser ? (
        <SettingsGroup
          title={t("web.settings.sessions.admin.groupTitle")}
          description={t("web.settings.sessions.admin.groupDescription")}
          flush
        >
          {adminRows.length ? (
            list(adminRows)
          ) : (
            <p className="text-muted-foreground py-4 text-sm">
              {t("web.settings.sessions.admin.empty")}
            </p>
          )}
          {!manageable && (
            <p className="text-muted-foreground pt-2 text-xs">
              {t("web.settings.sessions.admin.ownerOnly")}
            </p>
          )}
        </SettingsGroup>
      ) : (
        <>
          {current && (
            <SettingsGroup
              title={t("web.settings.sessions.current.title")}
              description={t("web.settings.sessions.current.description")}
              flush
            >
              {list([current])}
            </SettingsGroup>
          )}
          <SettingsGroup
            title={t("web.settings.sessions.others.title")}
            description={t("web.settings.sessions.others.description")}
            flush
          >
            {others.length ? (
              list(others)
            ) : (
              <div className="text-muted-foreground flex items-center gap-3 py-5 text-sm">
                <ShieldCheck className="size-5" aria-hidden="true" />
                <p>{t("web.settings.sessions.others.empty")}</p>
              </div>
            )}
          </SettingsGroup>
        </>
      )}
      {query.hasNextPage && !query.isError && (
        <Button
          variant="outline"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {query.isFetchingNextPage
            ? t("web.settings.sessions.loadingMore")
            : t("web.settings.sessions.loadMore")}
        </Button>
      )}
      <p className="text-muted-foreground text-xs leading-relaxed">
        {adminUser
          ? t("web.settings.sessions.footnoteAdmin")
          : t("web.settings.sessions.footnote", {
              link: (chunks) => (
                <Link to="/settings/account" className="underline underline-offset-4">
                  {chunks}
                </Link>
              ),
            })}
      </p>
      <AlertDialog
        open={Boolean(selection)}
        onOpenChange={(next) => {
          if (!next && !pending) setSelection(null);
        }}
      >
        <AlertDialogContent
          className={TOUCH_TARGETS}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            const target =
              !signedOut.current && returnFocus.current?.isConnected
                ? returnFocus.current
                : heading.current;
            target?.focus();
          }}
        >
          <AlertDialogHeader>
            <AlertDialogTitle>
              {selection?.kind === "all"
                ? t("web.settings.sessions.confirm.allTitle", {
                    username: adminUser?.username ?? "",
                  })
                : selection?.session.current
                  ? t("web.settings.sessions.confirm.thisBrowserTitle")
                  : selection
                    ? t("web.settings.sessions.confirm.deviceTitle", {
                        device: loginSessionDevice(selection.session).name,
                      })
                    : t("web.settings.sessions.confirm.sessionTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {selection?.kind === "all"
                ? t(
                    current
                      ? "web.settings.sessions.confirm.allDescriptionWithThisBrowser"
                      : "web.settings.sessions.confirm.allDescription",
                    { username: adminUser?.username ?? "" },
                  )
                : selection?.session.current
                  ? t("web.settings.sessions.confirm.thisBrowserDescription")
                  : adminUser
                    ? t("web.settings.sessions.confirm.adminClientDescription", {
                        username: adminUser.username,
                      })
                    : t("web.settings.sessions.confirm.clientDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <p className="text-muted-foreground text-xs">{t("web.settings.sessions.confirm.note")}</p>
          {actionError && (
            <p role="alert" className="text-destructive text-sm">
              {actionError}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={pending}
              onClick={(event) => {
                event.preventDefault();
                void confirm();
              }}
            >
              {pending
                ? t("web.settings.sessions.confirm.signingOut")
                : selection?.kind === "all"
                  ? t("web.settings.sessions.signOutEverywhere")
                  : t("common.signOut")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
