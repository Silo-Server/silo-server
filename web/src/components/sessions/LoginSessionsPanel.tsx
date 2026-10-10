import { useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { Loader2, RefreshCw, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
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
import { LoginSessionRow } from "./LoginSessionRow";

type Selection = { kind: "one"; session: LoginSession } | { kind: "all" };

export function LoginSessionsPanel({
  adminUser,
  manageable = true,
}: {
  adminUser?: { id: number; username: string };
  manageable?: boolean;
}) {
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
            ? `${adminUser?.username} is signed out everywhere`
            : "Session signed out",
        );
      }
    } catch (error) {
      setActionError(
        error instanceof Error ? error.message : "Couldn't sign out the session. Try again.",
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
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-2">
          <h2
            ref={heading}
            tabIndex={-1}
            className={`${adminUser ? "text-lg" : "text-2xl sm:text-3xl"} font-semibold tracking-tight focus:outline-none`}
          >
            Signed-in sessions
          </h2>
          <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
            {adminUser
              ? `Browsers and apps signed in to ${adminUser.username}’s account.`
              : "See where your account is signed in, and sign out any session you don’t recognize."}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
            aria-label="Refresh sessions"
          >
            <RefreshCw
              className={query.isFetching ? "animate-spin" : undefined}
              aria-hidden="true"
            />{" "}
            Refresh
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
              Sign out everywhere
            </Button>
          )}
        </div>
      </div>
      {query.isPending ? (
        <p role="status" className="text-muted-foreground flex items-center gap-2 text-sm">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" /> Loading sessions…
        </p>
      ) : query.isError ? (
        <div role="alert" className="surface-panel-raised space-y-3 p-5">
          <p>Couldn’t load sessions. Try again.</p>
          <Button
            variant="outline"
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
          >
            Retry
          </Button>
        </div>
      ) : adminUser ? (
        <SettingsGroup
          title="Active sign-ins"
          description="Sign out one client, or end all of this account’s sign-ins."
          flush
        >
          {adminRows.length ? (
            list(adminRows)
          ) : (
            <p className="text-muted-foreground py-4 text-sm">
              No active sign-ins for this account.
            </p>
          )}
          {!manageable && (
            <p className="text-muted-foreground pt-2 text-xs">
              Only the server owner can sign out another admin or the owner.
            </p>
          )}
        </SettingsGroup>
      ) : (
        <>
          {current && (
            <SettingsGroup
              title="This browser"
              description="The session you’re using right now."
              flush
            >
              {list([current])}
            </SettingsGroup>
          )}
          <SettingsGroup
            title="Other signed-in sessions"
            description="Other browsers and apps with access to your account."
            flush
          >
            {others.length ? (
              list(others)
            ) : (
              <div className="text-muted-foreground flex items-center gap-3 py-5 text-sm">
                <ShieldCheck className="size-5" aria-hidden="true" />
                <p>No other signed-in sessions.</p>
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
          {query.isFetchingNextPage ? "Loading…" : "Load more sessions"}
        </Button>
      )}
      <p className="text-muted-foreground text-xs leading-relaxed">
        Last seen records authenticated activity, updated at most once a minute. Older sessions may
        have no recorded activity yet. Signing out keeps the password and saved device settings.{" "}
        {adminUser ? (
          "Other accounts stay signed in."
        ) : (
          <Link to="/settings/account" className="underline underline-offset-4">
            Manage your account’s password
          </Link>
        )}
      </p>
      <AlertDialog
        open={Boolean(selection)}
        onOpenChange={(next) => {
          if (!next && !pending) setSelection(null);
        }}
      >
        <AlertDialogContent
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
                ? `Sign ${adminUser?.username} out everywhere?`
                : selection?.session.current
                  ? "Sign out this browser?"
                  : `Sign out ${selection ? loginSessionDevice(selection.session).name : "this session"}?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {selection?.kind === "all"
                ? `Every active sign-in to ${adminUser?.username}’s account will lose access on its next authenticated request, including sessions beyond this list. The password stays the same and other accounts stay signed in.${current ? " This includes the browser you’re using." : ""}`
                : selection?.session.current
                  ? "You’ll return to the sign-in screen. You can sign in again with the same password or sign-in provider."
                  : `This ${adminUser ? `client on ${adminUser.username}’s account` : "client"} will lose access on its next authenticated request. Its other sessions and password stay unchanged.`}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <p className="text-muted-foreground text-xs">
            A video already playing may continue until its next request. Saved device settings are
            kept.
          </p>
          {actionError && (
            <p role="alert" className="text-destructive text-sm">
              {actionError}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={pending}
              onClick={(event) => {
                event.preventDefault();
                void confirm();
              }}
            >
              {pending
                ? "Signing out…"
                : selection?.kind === "all"
                  ? "Sign out everywhere"
                  : "Sign out"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
