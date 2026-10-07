import { useId, useMemo, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { KeyRound, Link2, Loader2, Unlink } from "lucide-react";

import type { AdminUser, PluginInstallation } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { AdminUserSetPasswordDialog } from "@/components/AdminUserSetPasswordDialog";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  useAdminUserIdentities,
  useExternalSignInCapabilities,
  useLinkAdminUserIdentity,
  useUnlinkAdminUserIdentity,
  useUpdateAdminUserSignIn,
  type AdminUserIdentity,
} from "@/hooks/queries/admin/externalSignIn";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { useAdminSettingValue } from "@/hooks/queries/admin/settings";
import { formatDateTime } from "@/lib/datetime";
import {
  adminSignInErrorText,
  authCapabilityOf,
  authPluginInstallations,
  authProviderName,
  identityCheckStatusText,
  isNetworkSignIn,
} from "@/lib/externalSignInAdmin";
import { cn } from "@/lib/utils";
import { FeedbackLine, type Feedback } from "@/components/admin/FeedbackLine";

function Panel({
  title,
  headingRef,
  actions,
  children,
}: {
  title: string;
  headingRef?: React.RefObject<HTMLHeadingElement | null>;
  actions?: React.ReactNode;
  children: React.ReactNode;
}) {
  const headingId = useId();
  return (
    <section
      aria-labelledby={headingId}
      className="surface-panel overflow-hidden rounded-2xl border-0"
    >
      <div className="border-border flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
        <h3
          id={headingId}
          ref={headingRef}
          tabIndex={headingRef ? -1 : undefined}
          className="text-sm font-medium focus:outline-none"
        >
          {title}
        </h3>
        {actions}
      </div>
      <div className="space-y-3 px-4 py-3">{children}</div>
    </section>
  );
}

function when(value: string | null | undefined, never: string): string {
  return value ? formatDateTime(value, { seconds: false }) : never;
}

/** One linked identity, with what the provider last said about it. */
function IdentityRow({
  identity,
  showCheck,
  canUnlink,
  onUnlink,
}: {
  identity: AdminUserIdentity;
  showCheck: boolean;
  canUnlink: boolean;
  onUnlink: () => void;
}) {
  const provider = identity.provider_name || "Provider not enabled";
  const who = [identity.username, identity.email].filter(Boolean).join(" · ");
  const signedOut = ["not_found", "disabled", "not_permitted"].includes(identity.last_check_status);
  return (
    <li className="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0">
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-sm font-medium">
          {provider}
          {who ? <span className="text-muted-foreground font-normal"> · {who}</span> : null}
        </p>
        {identity.display_name ? (
          <p className="text-muted-foreground text-xs">{identity.display_name}</p>
        ) : null}
        <p className="text-muted-foreground text-xs break-all">
          Subject: <code className="font-mono">{identity.external_subject}</code>
        </p>
        <p className="text-muted-foreground text-xs">
          Connected {when(identity.linked_at, "")} · Last sign-in{" "}
          {when(identity.last_sign_in_at, "never")}
        </p>
        {showCheck ? (
          <p
            className={cn(
              "text-xs",
              signedOut ? "text-amber-600 dark:text-amber-400" : "text-muted-foreground",
            )}
          >
            Last provider check: {identityCheckStatusText(identity.last_check_status)}
            {identity.last_checked_at ? ` · ${when(identity.last_checked_at, "")}` : ""}
          </p>
        ) : null}
      </div>
      {canUnlink ? (
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={onUnlink}
          aria-label={`Unlink ${provider}${identity.username ? ` (${identity.username})` : ""}`}
        >
          <Unlink aria-hidden="true" />
          Unlink
        </Button>
      ) : null}
    </li>
  );
}

/**
 * What linking does to the account's Silo password, as the dialog says it:
 * break-glass accounts and network sign-ins (such as Tailscale) keep it, and
 * other providers turn it off. Nothing is said about a password the account
 * doesn't have.
 */
function connectPasswordNote(
  user: AdminUser,
  installation: PluginInstallation | undefined,
): string {
  if (user.break_glass) return " As a break-glass account it keeps its Silo password.";
  if (installation && isNetworkSignIn(installation)) {
    return user.password_login ? " It keeps its Silo password too." : "";
  }
  return " Connecting turns off password sign-in for this account.";
}

/** Connects an account to a provider identity by the provider's exact subject. */
function LinkIdentityDialog({
  user,
  onClose,
  onLinked,
}: {
  user: AdminUser;
  onClose: () => void;
  onLinked: (message: string) => void;
}) {
  const installations = useAdminPluginInstallations();
  const link = useLinkAdminUserIdentity();
  const candidates = useMemo(
    () =>
      authPluginInstallations(installations.data).filter((installation) => {
        const capability = authCapabilityOf(installation);
        return installation.auth_bindings?.some((b) => b.capability_id === capability?.id);
      }),
    [installations.data],
  );
  const preferred =
    candidates.find((installation) => installation.auth_bindings?.some((b) => b.enabled)) ??
    candidates[0];
  const [installationId, setInstallationId] = useState<string>("");
  const selected = installationId || (preferred ? String(preferred.id) : "");
  const selectedInstallation = candidates.find((candidate) => String(candidate.id) === selected);
  const [subject, setSubject] = useState("");
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const providerId = useId();
  const subjectId = useId();
  const subjectHintId = useId();
  const usernameId = useId();
  const emailId = useId();
  const displayNameId = useId();

  function submit(event: FormEvent) {
    event.preventDefault();
    if (!selected) {
      setError("Choose a sign-in provider.");
      return;
    }
    if (subject.trim() === "") {
      setError("Enter the provider's subject for this person.");
      return;
    }
    setError(null);
    link.mutate(
      {
        userId: user.id,
        body: {
          installation_id: selected,
          // The exact subject: Silo never normalizes it, so only outer
          // whitespace from a paste is dropped.
          external_subject: subject.trim(),
          ...(username.trim() ? { username: username.trim() } : {}),
          ...(email.trim() ? { email: email.trim() } : {}),
          ...(displayName.trim() ? { display_name: displayName.trim() } : {}),
        },
      },
      {
        onSuccess: () => {
          onLinked(
            `${user.username} is connected to ${selectedInstallation ? authProviderName(selectedInstallation) : "the provider"}.`,
          );
          onClose();
        },
        onError: (err) => {
          const text =
            err instanceof V2ProblemError && err.problemType === "conflict"
              ? `${user.username} is already connected to this provider. Unlink that identity first.`
              : adminSignInErrorText(err, "Couldn't connect the identity.");
          setError(text);
        },
      },
    );
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !link.isPending) onClose();
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="space-y-4" noValidate>
          <DialogHeader>
            <DialogTitle>Connect a sign-in identity</DialogTitle>
            <DialogDescription>
              {user.username} will sign in with this provider account.
              {connectPasswordNote(user, selectedInstallation)}
            </DialogDescription>
          </DialogHeader>
          {error ? (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          ) : null}
          {installations.isLoading ? (
            <Skeleton className="h-9 w-full" />
          ) : candidates.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              No sign-in provider is set up. Set one up in{" "}
              <Link to="/admin/settings/sign-in" className="text-foreground underline">
                Settings → Sign-in
              </Link>{" "}
              first.
            </p>
          ) : (
            <div className="space-y-2">
              <Label htmlFor={providerId}>Provider</Label>
              <Select value={selected} onValueChange={setInstallationId}>
                <SelectTrigger id={providerId} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {candidates.map((installation) => (
                    <SelectItem key={installation.id} value={String(installation.id)}>
                      {authProviderName(installation)}
                      {installation.auth_bindings?.some((b) => b.enabled) ? "" : " (off)"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <div className="space-y-2">
            <Label htmlFor={subjectId}>Subject</Label>
            <Input
              id={subjectId}
              required
              value={subject}
              autoFocus
              autoComplete="off"
              spellCheck={false}
              aria-describedby={subjectHintId}
              aria-invalid={error !== null && subject.trim() === ""}
              onChange={(event) => setSubject(event.target.value)}
              className="font-mono text-xs"
              placeholder="https://id.example.com|8f14e45f"
            />
            <p id={subjectHintId} className="text-muted-foreground text-xs">
              The provider's exact ID for the person, as Silo stores it. OpenID Connect: the issuer
              and subject joined by | (Entra ID: tenant ID and object ID). LDAP: the directory's
              unique ID, such as entryUUID or objectGUID. Silo never matches on a name or email.
            </p>
          </div>
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="space-y-1.5">
              <Label htmlFor={usernameId}>Username</Label>
              <Input
                id={usernameId}
                value={username}
                onChange={(event) => setUsername(event.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor={emailId}>Email</Label>
              <Input
                id={emailId}
                type="email"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor={displayNameId}>Name</Label>
              <Input
                id={displayNameId}
                value={displayName}
                onChange={(event) => setDisplayName(event.target.value)}
              />
            </div>
          </div>
          <p className="text-muted-foreground text-xs">
            Optional. Shown until the person's first sign-in replaces them with what the provider
            says.
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={link.isPending}>
              Cancel
            </Button>
            <Button type="submit" disabled={link.isPending || candidates.length === 0}>
              {link.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
              Connect
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/**
 * An account's Sign-in tab: whether it signs in with a Silo password (and a
 * way to set one when it can't), the break-glass flag for admins, and the
 * sign-in provider identities linked to it.
 */
export function AdminUserSignIn({
  user,
  manageable,
  viewerIsOwner,
  passwordOpen: passwordOpenProp,
  onPasswordOpenChange,
}: {
  user: AdminUser;
  /** The viewer may change this account (the Owner rule for admins). */
  manageable: boolean;
  viewerIsOwner: boolean;
  /**
   * Controls the Set password dialog, so the page header's Set password
   * can open it; the tab owns it when left out.
   */
  passwordOpen?: boolean;
  onPasswordOpenChange?: (open: boolean) => void;
}) {
  const capabilities = useExternalSignInCapabilities();
  const caps = capabilities.data;
  const identitiesServed = caps?.admin_identities === true;
  const identities = useAdminUserIdentities(user.id, identitiesServed);
  const unlink = useUnlinkAdminUserIdentity();
  const updateSignIn = useUpdateAdminUserSignIn();
  const [feedback, setFeedback] = useState<Feedback>(null);
  const [linkOpen, setLinkOpen] = useState(false);
  const [ownPasswordOpen, setOwnPasswordOpen] = useState(false);
  const passwordOpen = passwordOpenProp ?? ownPasswordOpen;
  const setPasswordOpen = onPasswordOpenChange ?? setOwnPasswordOpen;
  const [unlinking, setUnlinking] = useState<AdminUserIdentity | null>(null);
  const statusRef = useRef<HTMLParagraphElement>(null);
  const identitiesHeadingRef = useRef<HTMLHeadingElement>(null);
  const breakGlassId = useId();
  const breakGlassHint = useId();

  const linked = identities.data ?? [];
  const isAdmin = user.role === "admin";
  const breakGlass = user.break_glass;
  // auth.local_password_login: while it is off only break-glass accounts sign
  // in with a Silo password, whatever the account's own setting says.
  const serverPasswordLoginOn = useAdminSettingValue("auth.local_password_login").data !== "false";
  const passwordWaitsForServer = user.password_login && !serverPasswordLoginOn && !breakGlass;

  function report(next: Feedback, focus: "status" | "identities" = "status") {
    setFeedback(next);
    requestAnimationFrame(() =>
      (focus === "identities" ? identitiesHeadingRef.current : statusRef.current)?.focus(),
    );
  }

  function confirmUnlink() {
    const identity = unlinking;
    if (!identity) return;
    unlink.mutate(
      { userId: user.id, identityId: identity.id },
      {
        onSuccess: () =>
          report(
            {
              tone: "ok",
              text: `Unlinked ${identity.provider_name || "the identity"} from ${user.username}.`,
            },
            "identities",
          ),
        onError: (error) =>
          report({ tone: "error", text: adminSignInErrorText(error, "Couldn't unlink.") }),
      },
    );
  }

  function changeBreakGlass(next: boolean) {
    updateSignIn.mutate(
      { userId: user.id, body: { break_glass: next } },
      {
        onSuccess: () =>
          report({
            tone: "ok",
            text: next
              ? `${user.username} is a break-glass account.`
              : `${user.username} is no longer a break-glass account.`,
          }),
        onError: (error) =>
          report({
            tone: "error",
            text: adminSignInErrorText(error, "Couldn't change break-glass."),
          }),
      },
    );
  }

  const lastWay =
    !user.password_login && !breakGlass && linked.length === 1
      ? ` ${user.username} won't be able to sign in until you set a password or connect a provider again.`
      : "";

  return (
    <div className="space-y-6">
      <FeedbackLine feedback={feedback} focusRef={statusRef} />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Panel title="Password sign-in">
          <p className="text-sm">
            {!user.password_login
              ? `Off: ${user.username} can't sign in with a Silo password.`
              : passwordWaitsForServer
                ? `On for this account, but password sign-in is off for the server, so ${user.username} can't sign in with a Silo password until it's back on.`
                : `On: ${user.username} can sign in with a Silo password.`}
          </p>
          {passwordWaitsForServer ? (
            <p className="text-muted-foreground text-xs">
              Turn it back on in{" "}
              <Link to="/admin/settings/sign-in" className="text-foreground underline">
                Settings → Sign-in
              </Link>
              , or make the account break-glass.
            </p>
          ) : null}
          {user.password_login ? (
            <p className="text-muted-foreground text-xs">
              Connecting a sign-in provider turns this off, unless the account is break-glass.
            </p>
          ) : (
            <>
              <p className="text-muted-foreground text-xs">
                Setting a password turns it back on, for example when the account's provider is
                gone.
              </p>
              {manageable ? (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => setPasswordOpen(true)}
                  disabled={!user.enabled}
                >
                  <KeyRound aria-hidden="true" />
                  Set a password
                </Button>
              ) : null}
            </>
          )}
        </Panel>

        {caps?.break_glass === true && isAdmin ? (
          <Panel title="Break-glass">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0 space-y-1">
                <Label htmlFor={breakGlassId}>Break-glass account</Label>
                <p id={breakGlassHint} className="text-muted-foreground text-xs">
                  Keeps password sign-in when it's turned off for the server, so this admin can
                  still get in if the provider is down. Connecting a provider leaves its password
                  on, and the provider never demotes it.
                </p>
              </div>
              <Switch
                id={breakGlassId}
                aria-describedby={breakGlassHint}
                checked={breakGlass}
                disabled={!viewerIsOwner || updateSignIn.isPending}
                onCheckedChange={changeBreakGlass}
              />
            </div>
            {user.is_owner ? (
              <p className="text-muted-foreground text-xs">
                The server owner is break-glass by default. Turn this off only if your sign-in
                provider can recover the account.
              </p>
            ) : null}
            {!viewerIsOwner ? (
              <p className="text-muted-foreground text-xs">
                Only the server owner can change this.
              </p>
            ) : null}
            {breakGlass && !user.password_login ? (
              <p className="text-xs text-amber-600 dark:text-amber-400">
                This account can't sign in with a password, so it can't act as break-glass. Set a
                password for it.
              </p>
            ) : null}
          </Panel>
        ) : null}
      </div>

      {identitiesServed ? (
        <Panel
          title="Sign-in provider identities"
          headingRef={identitiesHeadingRef}
          actions={
            manageable ? (
              <Button type="button" size="sm" variant="outline" onClick={() => setLinkOpen(true)}>
                <Link2 aria-hidden="true" />
                Connect identity
              </Button>
            ) : null
          }
        >
          {identities.isLoading ? (
            <Skeleton className="h-12 w-full" />
          ) : identities.isError ? (
            <p role="alert" className="text-destructive text-sm">
              {adminSignInErrorText(identities.error, "Couldn't load the identities.")}
            </p>
          ) : linked.length === 0 ? (
            <p className="text-muted-foreground text-sm">Not connected to a sign-in provider.</p>
          ) : (
            <ul className="divide-border divide-y" aria-label="Connected identities">
              {linked.map((identity) => (
                <IdentityRow
                  key={identity.id}
                  identity={identity}
                  showCheck={caps?.provider_recheck === true}
                  canUnlink={manageable}
                  onUnlink={() => setUnlinking(identity)}
                />
              ))}
            </ul>
          )}
          {!manageable ? (
            <p className="text-muted-foreground text-xs">
              Only the server owner can change another admin's sign-in.
            </p>
          ) : null}
        </Panel>
      ) : null}

      <ConfirmDialog
        open={unlinking !== null}
        onOpenChange={(open) => {
          if (!open) setUnlinking(null);
        }}
        title={`Unlink ${unlinking?.provider_name || "this identity"}?`}
        description={`${user.username} can no longer sign in with this provider account.${lastWay}`}
        confirmLabel="Unlink"
        variant="destructive"
        onConfirm={confirmUnlink}
        isPending={unlink.isPending}
      />
      {linkOpen ? (
        <LinkIdentityDialog
          user={user}
          onClose={() => setLinkOpen(false)}
          onLinked={(text) => report({ tone: "ok", text }, "identities")}
        />
      ) : null}
      {passwordOpen ? (
        <AdminUserSetPasswordDialog
          user={user}
          serverPasswordLoginOn={serverPasswordLoginOn}
          onClose={() => setPasswordOpen(false)}
          onDone={(text) => report({ tone: "ok", text })}
        />
      ) : null}
    </div>
  );
}
