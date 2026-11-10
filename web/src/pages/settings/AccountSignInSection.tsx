import { useEffect, useState, type FormEvent, type ReactNode } from "react";
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
import { useT, type Translate } from "@/i18n";
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

// The text is formatted at render, so a banner follows a language change.
type Banner = { kind: "success" | "error"; text: (t: Translate) => string };

/** Readable text for a failed connect, by the problem the server answered. */
function connectErrorText(t: Translate, error: unknown, provider: AuthProviderOption): string {
  const fromStart = error instanceof LinkStartError;
  const problem = fromStart ? error.original : error;
  if (!(problem instanceof V2ProblemError)) {
    return t("web.settings.accountSignIn.error.unreachable");
  }
  if (problem.status === 429) {
    return t("web.settings.accountSignIn.error.tooManyAttempts");
  }
  const name = { provider: provider.display_name };
  const location = problem.problem.errors?.[0]?.location;
  switch (problem.problemType) {
    case "network_identity_required":
      return t("web.settings.accountSignIn.error.networkIdentityRequired", name);
    case "validation_failed":
      if (location === "body.directory_password") {
        return t("web.settings.accountSignIn.error.directoryRejected", name);
      }
      if (location === "body.password") {
        return t("web.settings.accountSignIn.error.wrongPassword");
      }
      break;
    case "local_password_required":
      return t("web.settings.accountSignIn.error.noLocalPassword");
    case "conflict":
      // link-start's 409: the page is not on the server's public URL.
      return t("web.settings.accountSignIn.error.publicAddressRequired");
    case "account_disabled":
      return t("web.settings.accountSignIn.error.accountDisabled", name);
    case "password_expired":
      return t("web.settings.accountSignIn.error.passwordExpired", name);
    case "permission_denied":
      return t("web.settings.accountSignIn.error.permissionDenied");
    case "not_permitted":
    case "provider_unavailable":
      return oauthFailureText(problem.problemType);
    case "identity_linked_elsewhere":
    case "already_linked":
      return oauthLinkFailureText(problem.problemType);
    case "not_found":
      return fromStart
        ? t("web.settings.accountSignIn.error.timedOut")
        : t("web.settings.accountSignIn.error.providerUnavailable", name);
  }
  return problem.message;
}

/** What connecting asks for, by how the provider signs in, and what changes after. */
function connectIntro(t: Translate, provider: AuthProviderOption): ReactNode {
  const name = { provider: provider.display_name };
  if (provider.mode === "network") {
    const owner = networkIdentityName(provider);
    return owner
      ? t("web.settings.accountSignIn.intro.networkOwner", {
          ...name,
          owner,
          name: (chunks) => chunks,
        })
      : t("web.settings.accountSignIn.intro.network", name);
  }
  if (provider.mode === "credentials") {
    return t("web.settings.accountSignIn.intro.credentials", name);
  }
  return t("web.settings.accountSignIn.intro.oauth", name);
}

/** Reads the result a web linking flow came back with, once. */
function linkResultBanner(params: URLSearchParams): Banner | null {
  if (params.get("linked") === "1") {
    return { kind: "success", text: (t) => t("web.settings.accountSignIn.linked") };
  }
  if (params.get("error") === "oauth_link_failed") {
    const reason = params.get("reason");
    return { kind: "error", text: () => oauthLinkFailureText(reason) };
  }
  return null;
}

/**
 * The account's "Sign-in" section: the external sign-in identity linked to
 * it, connecting the server's provider after re-entering the local password,
 * and disconnecting it while the account can still sign in another way.
 */
export function AccountSignInSection() {
  const t = useT();
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
      title={t("web.settings.accountSignIn.title")}
      description={t("web.settings.accountSignIn.description")}
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
            {banner.text(t)}
          </p>
        ) : null}

        {identities.isLoading ? (
          <div role="status" aria-label={t("web.settings.accountSignIn.loading")}>
            <Skeleton className="h-14 w-full" />
          </div>
        ) : identities.isError ? (
          <p className="text-destructive text-sm">{t("web.settings.accountSignIn.loadFailed")}</p>
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
                {t("web.settings.accountSignIn.impersonating")}
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
                        text: (translate) =>
                          translate("web.settings.accountSignIn.connected", {
                            provider: provider.display_name,
                          }),
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
                    {t("web.settings.accountSignIn.connectProvider", {
                      provider: provider.display_name,
                    })}
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

function identityProviderName(t: Translate, identity: AccountIdentity) {
  return identity.provider_name || t("web.settings.accountSignIn.providerTurnedOff");
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
  const t = useT();
  const who = [identity.username, identity.email].filter(Boolean);
  const lastSignIn = formatRelativeTime(identity.last_sign_in_at, { absoluteAfterDays: 30 });
  // The provider's latest answer about the identity: a sign-in, or the
  // server's periodic re-check that the account may still sign in.
  const lastChecked = formatRelativeTime(identity.last_checked_at, { absoluteAfterDays: 30 });
  const history = [
    t("web.settings.accountSignIn.history.connected", {
      date: formatDate(identity.linked_at, "medium"),
    }),
    lastSignIn
      ? t("web.settings.accountSignIn.history.lastSignIn", { time: lastSignIn })
      : t("web.settings.accountSignIn.history.neverUsed"),
    lastChecked ? t("web.settings.accountSignIn.history.lastChecked", { time: lastChecked }) : "",
  ].filter(Boolean);
  return (
    <div className="border-border/60 flex flex-wrap items-center justify-between gap-3 rounded-md border p-3">
      <div className="min-w-0 space-y-0.5 text-sm">
        <p className="font-medium">{identityProviderName(t, identity)}</p>
        {who.length > 0 ? <p className="break-all">{who.join(" · ")}</p> : null}
        <p className="text-muted-foreground text-xs">{history.join(" · ")}</p>
        {onlyWayIn ? (
          <p className="text-muted-foreground text-xs">
            {t("web.settings.accountSignIn.onlyWayIn")}
          </p>
        ) : null}
      </div>
      {canDisconnect ? (
        <Button type="button" variant="outline" size="sm" onClick={onDisconnect}>
          {t("web.settings.accountSignIn.disconnect")}
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
  const t = useT();
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
  const name = { provider: provider.display_name };

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
      setError(connectErrorText(t, failure, provider));
    }
  }

  return (
    <form
      className="border-border/60 space-y-4 rounded-md border p-4"
      onSubmit={(event) => void handleSubmit(event)}
      aria-label={t("web.settings.accountSignIn.connectProvider", name)}
    >
      <div className="space-y-1 text-sm">
        <p className="font-medium">{t("web.settings.accountSignIn.connectProvider", name)}</p>
        <p className="text-muted-foreground">{connectIntro(t, provider)}</p>
      </div>
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-password`}>
          {t("web.settings.accountSignIn.siloPassword")}
        </Label>
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
            <Label htmlFor={`${idPrefix}-username`}>
              {t("web.settings.accountSignIn.providerUsername", name)}
            </Label>
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
              {t("web.settings.accountSignIn.providerPassword", name)}
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
            ? t("web.settings.accountSignIn.connecting")
            : directory || network
              ? t("web.settings.accountSignIn.connect")
              : t("web.settings.accountSignIn.continueTo", name)}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          {t("common.cancel")}
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
  const t = useT();
  const unlink = useUnlinkAccountIdentity();
  const [error, setError] = useState<string | null>(null);
  const name = identity ? identityProviderName(t, identity) : "";

  async function disconnect() {
    if (!identity) return;
    setError(null);
    try {
      await unlink.mutateAsync(identity.id);
      toast.success(t("web.settings.accountSignIn.disconnected", { provider: name }));
      onClose();
    } catch (failure) {
      if (failure instanceof V2ProblemError && failure.problemType === "last_sign_in_method") {
        setError(t("web.settings.accountSignIn.error.lastMethod", { provider: name }));
      } else if (failure instanceof V2ProblemError && failure.status === 404) {
        // Already gone; the list refreshes.
        onClose();
      } else {
        setError(
          failure instanceof Error
            ? failure.message
            : t("web.settings.accountSignIn.error.disconnectFailed"),
        );
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
          <AlertDialogTitle>
            {t("web.settings.accountSignIn.disconnectTitle", { provider: name })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("web.settings.accountSignIn.disconnectDescription", { provider: name })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error ? (
          <p className="text-destructive text-sm" role="alert">
            {error}
          </p>
        ) : null}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={unlink.isPending}>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            disabled={unlink.isPending}
            onClick={(event) => {
              event.preventDefault();
              void disconnect();
            }}
          >
            {unlink.isPending
              ? t("web.settings.accountSignIn.disconnecting")
              : t("web.settings.accountSignIn.disconnect")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
