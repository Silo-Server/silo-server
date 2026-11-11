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
import { useT } from "@/i18n";
import { TOUCH_HIT_AREA, TOUCH_TARGETS } from "@/lib/touchTargets";
import { cn } from "@/lib/utils";
import { FeedbackLine, type Feedback } from "@/components/admin/FeedbackLine";

const SEPARATOR = " · ";

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
  const t = useT();
  const provider =
    identity.provider_name || t("web.admin.common.adminUserSignIn.provider.notEnabled");
  const who = [identity.username, identity.email].filter(Boolean).join(" · ");
  const signedOut = ["not_found", "disabled", "not_permitted"].includes(identity.last_check_status);
  return (
    <li className="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0">
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-sm font-medium break-words">
          {provider}
          {who ? (
            <span className="text-muted-foreground font-normal">
              {SEPARATOR}
              {who}
            </span>
          ) : null}
        </p>
        {identity.display_name ? (
          <p className="text-muted-foreground text-xs break-words">{identity.display_name}</p>
        ) : null}
        <p className="text-muted-foreground text-xs break-all">
          {t("web.admin.common.adminUserSignIn.identity.subject", {
            subject: identity.external_subject,
            code: (chunks) => <code className="font-mono">{chunks}</code>,
          })}
        </p>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.common.adminUserSignIn.identity.connected", {
            linked: when(identity.linked_at, ""),
            signed_in: identity.last_sign_in_at ? "yes" : "no",
            last_sign_in: when(identity.last_sign_in_at, ""),
          })}
        </p>
        {showCheck ? (
          <p
            className={cn(
              "text-xs",
              signedOut ? "text-amber-600 dark:text-amber-400" : "text-muted-foreground",
            )}
          >
            {t("web.admin.common.adminUserSignIn.identity.lastCheck", {
              checked: identity.last_checked_at ? "yes" : "no",
              status: identityCheckStatusText(identity.last_check_status),
              checked_at: when(identity.last_checked_at, ""),
            })}
          </p>
        ) : null}
      </div>
      {canUnlink ? (
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={onUnlink}
          aria-label={
            identity.username
              ? t("web.admin.common.adminUserSignIn.identity.unlinkLabelWithUser", {
                  provider,
                  username: identity.username,
                })
              : t("web.admin.common.adminUserSignIn.identity.unlinkLabel", { provider })
          }
        >
          <Unlink aria-hidden="true" />
          {t("web.admin.common.adminUserSignIn.unlink")}
        </Button>
      ) : null}
    </li>
  );
}

/**
 * What linking does to the account's Silo password, as the dialog says it:
 * break-glass accounts and network sign-ins (such as Tailscale) keep it, and
 * other providers turn it off. Nothing is said about a password the account
 * doesn't have. The value selects the note in the dialog's description.
 */
function connectPasswordNote(
  user: AdminUser,
  installation: PluginInstallation | undefined,
): "breakGlass" | "keeps" | "turnsOff" | "none" {
  if (user.break_glass) return "breakGlass";
  if (installation && isNetworkSignIn(installation)) {
    return user.password_login ? "keeps" : "none";
  }
  return "turnsOff";
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
  const t = useT();
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
      setError(t("web.admin.common.adminUserSignIn.link.chooseProvider"));
      return;
    }
    if (subject.trim() === "") {
      setError(t("web.admin.common.adminUserSignIn.link.enterSubject"));
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
            selectedInstallation
              ? t("web.admin.common.adminUserSignIn.link.connected", {
                  username: user.username,
                  provider: authProviderName(selectedInstallation),
                })
              : t("web.admin.common.adminUserSignIn.link.connectedUnknownProvider", {
                  username: user.username,
                }),
          );
          onClose();
        },
        onError: (err) => {
          const text =
            err instanceof V2ProblemError && err.problemType === "conflict"
              ? t("web.admin.common.adminUserSignIn.link.alreadyConnected", {
                  username: user.username,
                })
              : adminSignInErrorText(err, t("web.admin.common.adminUserSignIn.link.failed"));
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
      <DialogContent className={cn("sm:max-w-lg", TOUCH_TARGETS)}>
        <form onSubmit={submit} className="space-y-4" noValidate>
          <DialogHeader>
            <DialogTitle>{t("web.admin.common.adminUserSignIn.link.title")}</DialogTitle>
            <DialogDescription>
              {t("web.admin.common.adminUserSignIn.link.description", {
                username: user.username,
                note: connectPasswordNote(user, selectedInstallation),
              })}
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
              {t("web.admin.common.adminUserSignIn.link.noProviders", {
                link: (chunks) => (
                  <Link
                    to="/admin/settings/sign-in"
                    className={cn("text-foreground underline", TOUCH_HIT_AREA)}
                  >
                    {chunks}
                  </Link>
                ),
              })}
            </p>
          ) : (
            <div className="space-y-2">
              <Label htmlFor={providerId}>
                {t("web.admin.common.adminUserSignIn.link.provider")}
              </Label>
              <Select value={selected} onValueChange={setInstallationId}>
                <SelectTrigger id={providerId} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent className={TOUCH_TARGETS}>
                  {candidates.map((installation) => (
                    <SelectItem key={installation.id} value={String(installation.id)}>
                      {installation.auth_bindings?.some((b) => b.enabled)
                        ? authProviderName(installation)
                        : t("web.admin.common.adminUserSignIn.link.providerOff", {
                            provider: authProviderName(installation),
                          })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <div className="space-y-2">
            <Label htmlFor={subjectId}>{t("web.admin.common.adminUserSignIn.link.subject")}</Label>
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
              placeholder={t("web.admin.common.adminUserSignIn.link.subjectPlaceholder")}
            />
            <p id={subjectHintId} className="text-muted-foreground text-xs">
              {t("web.admin.common.adminUserSignIn.link.subjectHint")}
            </p>
          </div>
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="space-y-1.5">
              <Label htmlFor={usernameId}>
                {t("web.admin.common.adminUserSignIn.link.username")}
              </Label>
              <Input
                id={usernameId}
                value={username}
                onChange={(event) => setUsername(event.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor={emailId}>{t("web.admin.common.adminUserSignIn.link.email")}</Label>
              <Input
                id={emailId}
                type="email"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor={displayNameId}>
                {t("web.admin.common.adminUserSignIn.link.name")}
              </Label>
              <Input
                id={displayNameId}
                value={displayName}
                onChange={(event) => setDisplayName(event.target.value)}
              />
            </div>
          </div>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.common.adminUserSignIn.link.optionalHint")}
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={link.isPending}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={link.isPending || candidates.length === 0}>
              {link.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
              {t("web.admin.common.adminUserSignIn.link.connect")}
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
  const t = useT();
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
              text: identity.provider_name
                ? t("web.admin.common.adminUserSignIn.unlinked", {
                    provider: identity.provider_name,
                    username: user.username,
                  })
                : t("web.admin.common.adminUserSignIn.unlinkedUnknownProvider", {
                    username: user.username,
                  }),
            },
            "identities",
          ),
        onError: (error) =>
          report({
            tone: "error",
            text: adminSignInErrorText(error, t("web.admin.common.adminUserSignIn.unlinkFailed")),
          }),
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
              ? t("web.admin.common.adminUserSignIn.breakGlass.on", { username: user.username })
              : t("web.admin.common.adminUserSignIn.breakGlass.off", { username: user.username }),
          }),
        onError: (error) =>
          report({
            tone: "error",
            text: adminSignInErrorText(
              error,
              t("web.admin.common.adminUserSignIn.breakGlass.failed"),
            ),
          }),
      },
    );
  }

  const lastWay = !user.password_login && !breakGlass && linked.length === 1;

  return (
    <div className="space-y-6">
      <FeedbackLine feedback={feedback} focusRef={statusRef} />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Panel title={t("web.admin.common.adminUserSignIn.password.title")}>
          <p className="text-sm">
            {!user.password_login
              ? t("web.admin.common.adminUserSignIn.password.off", { username: user.username })
              : passwordWaitsForServer
                ? t("web.admin.common.adminUserSignIn.password.waitsForServer", {
                    username: user.username,
                  })
                : t("web.admin.common.adminUserSignIn.password.on", { username: user.username })}
          </p>
          {passwordWaitsForServer ? (
            <p className="text-muted-foreground text-xs">
              {t("web.admin.common.adminUserSignIn.password.turnBackOn", {
                link: (chunks) => (
                  <Link
                    to="/admin/settings/sign-in"
                    className={cn("text-foreground underline", TOUCH_HIT_AREA)}
                  >
                    {chunks}
                  </Link>
                ),
              })}
            </p>
          ) : null}
          {user.password_login ? (
            <p className="text-muted-foreground text-xs">
              {t("web.admin.common.adminUserSignIn.password.connectingTurnsOff")}
            </p>
          ) : (
            <>
              <p className="text-muted-foreground text-xs">
                {t("web.admin.common.adminUserSignIn.password.settingTurnsOn")}
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
                  {t("web.admin.common.adminUserSignIn.password.set")}
                </Button>
              ) : null}
            </>
          )}
        </Panel>

        {caps?.break_glass === true && isAdmin ? (
          <Panel title={t("web.admin.common.adminUserSignIn.breakGlass.title")}>
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0 space-y-1">
                <Label htmlFor={breakGlassId}>
                  {t("web.admin.common.adminUserSignIn.breakGlass.label")}
                </Label>
                <p id={breakGlassHint} className="text-muted-foreground text-xs">
                  {t("web.admin.common.adminUserSignIn.breakGlass.hint")}
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
                {t("web.admin.common.adminUserSignIn.breakGlass.ownerDefault")}
              </p>
            ) : null}
            {!viewerIsOwner ? (
              <p className="text-muted-foreground text-xs">
                {t("web.admin.common.adminUserSignIn.breakGlass.ownerOnly")}
              </p>
            ) : null}
            {breakGlass && !user.password_login ? (
              <p className="text-xs text-amber-600 dark:text-amber-400">
                {t("web.admin.common.adminUserSignIn.breakGlass.noPassword")}
              </p>
            ) : null}
          </Panel>
        ) : null}
      </div>

      {identitiesServed ? (
        <Panel
          title={t("web.admin.common.adminUserSignIn.identities.title")}
          headingRef={identitiesHeadingRef}
          actions={
            manageable ? (
              <Button type="button" size="sm" variant="outline" onClick={() => setLinkOpen(true)}>
                <Link2 aria-hidden="true" />
                {t("web.admin.common.adminUserSignIn.identities.connect")}
              </Button>
            ) : null
          }
        >
          {identities.isLoading ? (
            <Skeleton className="h-12 w-full" />
          ) : identities.isError ? (
            <p role="alert" className="text-destructive text-sm">
              {adminSignInErrorText(
                identities.error,
                t("web.admin.common.adminUserSignIn.identities.loadFailed"),
              )}
            </p>
          ) : linked.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              {t("web.admin.common.adminUserSignIn.identities.empty")}
            </p>
          ) : (
            <ul
              className="divide-border divide-y"
              aria-label={t("web.admin.common.adminUserSignIn.identities.listLabel")}
            >
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
              {t("web.admin.common.adminUserSignIn.identities.ownerOnly")}
            </p>
          ) : null}
        </Panel>
      ) : null}

      <ConfirmDialog
        open={unlinking !== null}
        onOpenChange={(open) => {
          if (!open) setUnlinking(null);
        }}
        title={
          unlinking?.provider_name
            ? t("web.admin.common.adminUserSignIn.unlinkConfirm.title", {
                provider: unlinking.provider_name,
              })
            : t("web.admin.common.adminUserSignIn.unlinkConfirm.titleUnknownProvider")
        }
        description={t("web.admin.common.adminUserSignIn.unlinkConfirm.description", {
          username: user.username,
          last_way: lastWay ? "yes" : "no",
        })}
        confirmLabel={t("web.admin.common.adminUserSignIn.unlink")}
        className={TOUCH_TARGETS}
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
