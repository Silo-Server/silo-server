import { useEffect, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import { toast } from "sonner";

import { V2ProblemError } from "@/api/v2/request";
import { captureSessionIdentity, isSessionIdentityCurrent } from "@/api/client";
import { PasswordInput } from "@/components/PasswordInput";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
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
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  LinkStartError,
  useAccountIdentities,
  useLinkAccountIdentityWithCredentials,
  useLinkAccountIdentityWithNetwork,
  useStartAccountIdentityLink,
  useUnlinkAccountIdentity,
  type AccountIdentity,
} from "@/hooks/queries/account";
import { useAuth, type AuthProviderOption } from "@/hooks/useAuth";
import { formatRelativeTime } from "@/lib/date";
import { formatDate } from "@/lib/datetime";
import {
  leaveForProvider,
  networkIdentityName,
  oauthFailureText,
  oauthLinkFailureText,
} from "@/lib/externalSignIn";

// Where a web linking flow comes back to, with linked=1 or
// error=oauth_link_failed&reason=<reason>.
const LINK_RETURN_PATH = "/settings/account";

type Banner = { kind: "success"; text: string } | { kind: "error"; text: string };

const NO_LOCAL_PASSWORD =
  "Your account has no Silo password to confirm this with. Ask an admin to connect the provider for you.";

/** Readable text for a failed connect, by the problem the server answered. */
function connectErrorText(error: unknown, provider: AuthProviderOption): string {
  const fromStart = error instanceof LinkStartError;
  const problem = fromStart ? error.original : error;
  if (!(problem instanceof V2ProblemError)) {
    return "Couldn't reach the server. Try again.";
  }
  if (problem.status === 429) {
    return "Too many attempts. Wait a minute and try again.";
  }
  const location = problem.problem.errors?.[0]?.location;
  switch (problem.problemType) {
    case "network_identity_required":
      return `Open this server at its ${provider.display_name} address to connect ${provider.display_name}.`;
    case "validation_failed":
      if (location === "body.directory_password") {
        return `${provider.display_name} didn't accept that username and password.`;
      }
      if (location === "body.password") {
        return "That isn't your current Silo password.";
      }
      break;
    case "local_password_required":
      return NO_LOCAL_PASSWORD;
    case "conflict":
      // link-start's 409: the page is not on the server's public URL.
      return "Open this server at its public address to connect a sign-in provider.";
    case "account_disabled":
      return `Your ${provider.display_name} account is disabled.`;
    case "password_expired":
      return `Your ${provider.display_name} password has expired. Change it there, then try again.`;
    case "permission_denied":
      return "This session can't change how the account signs in. Sign in to the account itself and try again.";
    case "not_permitted":
    case "provider_unavailable":
      return oauthFailureText(problem.problemType);
    case "identity_linked_elsewhere":
    case "already_linked":
      return oauthLinkFailureText(problem.problemType);
    case "not_found":
      return fromStart
        ? "That took too long. Try again."
        : `${provider.display_name} isn't available right now.`;
  }
  return problem.message;
}

/** What connecting asks for, by how the provider signs in. */
function connectIntro(provider: AuthProviderOption): string {
  const name = provider.display_name;
  if (provider.mode === "network") {
    const owner = networkIdentityName(provider);
    const account = owner ? `${owner}'s ${name} account` : `the ${name} account`;
    return `Confirm your Silo password to connect ${account} on this device.`;
  }
  if (provider.mode === "credentials") {
    return `Confirm your Silo password, then enter your ${name} username and password.`;
  }
  return `Confirm your Silo password, then sign in at ${name}.`;
}

/** Reads the result a web linking flow came back with, once. */
function linkResultBanner(params: URLSearchParams): Banner | null {
  if (params.get("linked") === "1") {
    return { kind: "success", text: "Your account is connected to the sign-in provider." };
  }
  if (params.get("error") === "oauth_link_failed") {
    return { kind: "error", text: oauthLinkFailureText(params.get("reason")) };
  }
  return null;
}

/**
 * The account's "Sign-in" section: the external sign-in identity linked to
 * it, connecting the server's provider after re-entering the local password,
 * and disconnecting it while the account can still sign in another way.
 */
export function AccountSignInSection() {
  const { providers = [], isImpersonating, refreshSignInProviders } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const [banner, setBanner] = useState<Banner | null>(() => linkResultBanner(searchParams));
  const [connecting, setConnecting] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState<AccountIdentity | null>(null);
  const identities = useAccountIdentities();

  useEffect(() => {
    void refreshSignInProviders?.();
  }, [refreshSignInProviders]);

  // Drop the flow's result from the address so a reload doesn't repeat it.
  useEffect(() => {
    if (!searchParams.has("linked") && !searchParams.has("error")) return;
    const next = new URLSearchParams(searchParams);
    next.delete("linked");
    next.delete("error");
    next.delete("reason");
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams]);

  const externalProviders = providers.filter(
    (entry) =>
      entry.installation_id &&
      (entry.mode === "oauth" || entry.mode === "credentials" || entry.mode === "network"),
  );
  const linked = identities.data?.items ?? [];
  const linkedInstallations = new Set(linked.map((identity) => identity.installation_id));
  // Disconnecting is offered only while the account can still sign in
  // another way: another identity, or a local password the server still
  // accepts (the server switch, or break-glass). The server answers its own
  // rule (last_sign_in_method) as can_unlink.
  const canDisconnect = identities.data?.can_unlink === true;
  const connectable = externalProviders.filter(
    (entry) => !linkedInstallations.has(entry.installation_id ?? ""),
  );

  if (externalProviders.length === 0 && linked.length === 0 && !banner) {
    return null;
  }

  return (
    <SettingsGroup
      title="Sign-in"
      description="Sign in to this account with your organization's sign-in provider."
    >
      <div className="max-w-2xl space-y-4">
        {banner ? (
          <p
            role={banner.kind === "error" ? "alert" : "status"}
            className={
              banner.kind === "error"
                ? "border-destructive/30 bg-destructive/10 text-destructive rounded-md border p-3 text-sm"
                : "border-border/60 bg-background/50 rounded-md border p-3 text-sm"
            }
          >
            {banner.text}
          </p>
        ) : null}

        {identities.isLoading ? (
          <div role="status" aria-label="Loading sign-in settings">
            <Skeleton className="h-14 w-full" />
          </div>
        ) : identities.isError ? (
          <p className="text-destructive text-sm">
            Sign-in settings could not be loaded. Refresh the page to try again.
          </p>
        ) : (
          <>
            {linked.map((identity) => (
              <LinkedIdentityRow
                key={identity.id}
                identity={identity}
                canDisconnect={!isImpersonating && canDisconnect}
                onlyWayIn={!canDisconnect}
                onDisconnect={() => setDisconnecting(identity)}
              />
            ))}

            {isImpersonating ? (
              <p className="text-muted-foreground text-sm">
                Only the account itself can change how it signs in, not while you view it as another
                user.
              </p>
            ) : (
              connectable.map((provider) =>
                connecting === provider.installation_id ? (
                  <ConnectForm
                    key={provider.id}
                    provider={provider}
                    onCancel={() => setConnecting(null)}
                    onLinked={() => {
                      setConnecting(null);
                      setBanner({
                        kind: "success",
                        text: `Connected ${provider.display_name}. Sign in with it from now on.`,
                      });
                    }}
                  />
                ) : (
                  <Button
                    key={provider.id}
                    type="button"
                    variant="outline"
                    className="gap-3"
                    onClick={() => {
                      setBanner(null);
                      setConnecting(provider.installation_id ?? null);
                    }}
                  >
                    {provider.icon_url ? (
                      <img src={provider.icon_url} alt="" className="h-5 w-5" />
                    ) : null}
                    Connect {provider.display_name}
                  </Button>
                ),
              )
            )}
          </>
        )}
      </div>

      <DisconnectDialog identity={disconnecting} onClose={() => setDisconnecting(null)} />
    </SettingsGroup>
  );
}

function identityProviderName(identity: AccountIdentity) {
  return identity.provider_name || "Sign-in provider (turned off)";
}

function LinkedIdentityRow({
  identity,
  canDisconnect,
  onlyWayIn,
  onDisconnect,
}: {
  identity: AccountIdentity;
  canDisconnect: boolean;
  onlyWayIn: boolean;
  onDisconnect: () => void;
}) {
  const who = [identity.username, identity.email].filter(Boolean);
  const lastSignIn = formatRelativeTime(identity.last_sign_in_at, { absoluteAfterDays: 30 });
  // The provider's latest answer about the identity: a sign-in, or the
  // server's periodic re-check that the account may still sign in.
  const lastChecked = formatRelativeTime(identity.last_checked_at, { absoluteAfterDays: 30 });
  const history = [
    `Connected ${formatDate(identity.linked_at, "medium")}`,
    lastSignIn ? `Last sign-in ${lastSignIn}` : "Not used to sign in yet",
    lastChecked ? `Last checked ${lastChecked}` : "",
  ].filter(Boolean);
  return (
    <div className="border-border/60 flex flex-wrap items-center justify-between gap-3 rounded-md border p-3">
      <div className="min-w-0 space-y-0.5 text-sm">
        <p className="font-medium">{identityProviderName(identity)}</p>
        {who.length > 0 ? <p className="break-all">{who.join(" · ")}</p> : null}
        <p className="text-muted-foreground text-xs">{history.join(" · ")}</p>
        {onlyWayIn ? (
          <p className="text-muted-foreground text-xs">
            This is how you sign in to this account, so it stays connected.
          </p>
        ) : null}
      </div>
      {canDisconnect ? (
        <Button type="button" variant="outline" size="sm" onClick={onDisconnect}>
          Disconnect
        </Button>
      ) : null}
    </div>
  );
}

function ConnectForm({
  provider,
  onCancel,
  onLinked,
}: {
  provider: AuthProviderOption;
  onCancel: () => void;
  onLinked: () => void;
}) {
  const startLink = useStartAccountIdentityLink();
  const linkCredentials = useLinkAccountIdentityWithCredentials();
  const linkNetwork = useLinkAccountIdentityWithNetwork();
  const [password, setPassword] = useState("");
  const [directoryUsername, setDirectoryUsername] = useState("");
  const [directoryPassword, setDirectoryPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [leaving, setLeaving] = useState(false);
  const installationId = provider.installation_id ?? "";
  const directory = provider.mode === "credentials";
  // A network provider (such as Tailscale) names the owner of this device;
  // only the Silo password is asked for.
  const network = provider.mode === "network";
  const busy = startLink.isPending || linkCredentials.isPending || linkNetwork.isPending || leaving;
  const idPrefix = `connect-${installationId}`;

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    const session = captureSessionIdentity();
    try {
      if (network) {
        await linkNetwork.mutateAsync({ installation_id: installationId, password });
        if (!isSessionIdentityCurrent(session)) return;
        onLinked();
        return;
      }
      if (directory) {
        await linkCredentials.mutateAsync({
          installation_id: installationId,
          password,
          username: directoryUsername,
          directory_password: directoryPassword,
        });
        if (!isSessionIdentityCurrent(session)) return;
        onLinked();
        return;
      }
      const started = await startLink.mutateAsync({
        installationId,
        password,
        next: LINK_RETURN_PATH,
      });
      if (!isSessionIdentityCurrent(session)) return;
      if (!/^https?:\/\//i.test(started.authorize_url)) {
        setError(oauthFailureText("provider_unavailable"));
        return;
      }
      setLeaving(true);
      leaveForProvider(started.authorize_url);
    } catch (failure) {
      setError(connectErrorText(failure, provider));
    }
  }

  return (
    <form
      className="border-border/60 space-y-4 rounded-md border p-4"
      onSubmit={(event) => void handleSubmit(event)}
      aria-label={`Connect ${provider.display_name}`}
    >
      <div className="space-y-1 text-sm">
        <p className="font-medium">Connect {provider.display_name}</p>
        <p className="text-muted-foreground">
          {connectIntro(provider)} Afterwards you sign in with {provider.display_name} instead of
          your Silo password.
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-password`}>Silo password</Label>
        <PasswordInput
          id={`${idPrefix}-password`}
          autoComplete="current-password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          required
        />
      </div>
      {directory ? (
        <>
          <div className="space-y-2">
            <Label htmlFor={`${idPrefix}-username`}>{provider.display_name} username</Label>
            <Input
              id={`${idPrefix}-username`}
              autoComplete="off"
              value={directoryUsername}
              onChange={(event) => setDirectoryUsername(event.target.value)}
              maxLength={256}
              required
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor={`${idPrefix}-directory-password`}>
              {provider.display_name} password
            </Label>
            <PasswordInput
              id={`${idPrefix}-directory-password`}
              autoComplete="off"
              value={directoryPassword}
              onChange={(event) => setDirectoryPassword(event.target.value)}
              required
            />
          </div>
        </>
      ) : null}
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={busy}>
          {busy
            ? "Connecting…"
            : directory || network
              ? "Connect"
              : `Continue to ${provider.display_name}`}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function DisconnectDialog({
  identity,
  onClose,
}: {
  identity: AccountIdentity | null;
  onClose: () => void;
}) {
  const unlink = useUnlinkAccountIdentity();
  const [error, setError] = useState<string | null>(null);
  const name = identity ? identityProviderName(identity) : "";

  async function disconnect() {
    if (!identity) return;
    setError(null);
    try {
      await unlink.mutateAsync(identity.id);
      toast.success(`Disconnected ${name}`);
      onClose();
    } catch (failure) {
      if (failure instanceof V2ProblemError && failure.problemType === "last_sign_in_method") {
        setError(
          `${name} is the only way to sign in to your account, so it can't be disconnected. Ask an admin to set a Silo password for you first.`,
        );
      } else if (failure instanceof V2ProblemError && failure.status === 404) {
        // Already gone; the list refreshes.
        onClose();
      } else {
        setError(failure instanceof Error ? failure.message : "Couldn't disconnect. Try again.");
      }
    }
  }

  return (
    <AlertDialog
      open={identity !== null}
      onOpenChange={(open) => {
        if (!open && !unlink.isPending) {
          setError(null);
          onClose();
        }
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Disconnect {name}?</AlertDialogTitle>
          <AlertDialogDescription>
            You won&apos;t be able to sign in with {name} anymore. Afterwards, sign in with your
            Silo password. The server only allows this while your account can still sign in another
            way.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error ? (
          <p className="text-destructive text-sm" role="alert">
            {error}
          </p>
        ) : null}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={unlink.isPending}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={unlink.isPending}
            onClick={(event) => {
              event.preventDefault();
              void disconnect();
            }}
          >
            {unlink.isPending ? "Disconnecting…" : "Disconnect"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
