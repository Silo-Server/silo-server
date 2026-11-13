import { useEffect, useMemo, useState } from "react";
import {
  Cast,
  Check,
  ChevronDown,
  Copy,
  KeyRound,
  LogIn,
  MonitorSmartphone,
  Server,
  Tv,
  TriangleAlert,
  Users,
  X,
} from "lucide-react";
import { toast } from "sonner";

import type { Profile } from "@/api/types";
import { PageHeader } from "@/components/page/PageHeader";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useT } from "@/i18n";
import { useAuth } from "@/hooks/useAuth";
import { useAccountIdentities } from "@/hooks/queries/account";
import { useAuthProviders } from "@/hooks/queries/authProviders";
import { useCompatConnectInfo } from "@/hooks/queries/compat";
import { useProfiles } from "@/hooks/queries/profiles";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";
import {
  buildJellyfinPasswordHint,
  buildJellyfinUsername,
  isLoopbackURL,
  jellyfinUsernameIssue,
  resolveSiloSignIn,
} from "./connectApps";

type AppKind = "silo" | "jellyfin";

/** The account/profile and password/PIN separator: syntax, not text to translate. */
const SEPARATOR = "#";

/** Renders `name#Profile` with the separator tinted so `#` reads as structure. */
function HashString({ before, after }: { before: string; after: string }) {
  return (
    <span className="font-mono">
      {before}
      <span className="text-info font-semibold">{SEPARATOR}</span>
      {after}
    </span>
  );
}

interface FieldRowProps {
  label: string;
  icon: typeof Server;
  kind: AppKind;
  hint?: string;
  /** Rendered value. Omit copyValue for patterns that aren't literal text. */
  children: React.ReactNode;
  copyValue?: string;
}

function FieldRow({ label, icon: Icon, kind, hint, children, copyValue }: FieldRowProps) {
  const t = useT();
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);

  async function handleCopy() {
    if (!copyValue) return;
    try {
      await copyTextToClipboard(copyValue);
      setCopied(true);
    } catch {
      toast.error(t("web.settings.connectApps.copyFailed"));
    }
  }

  return (
    <div
      className={cn(
        "rounded-md border px-3.5 py-2.5",
        kind === "jellyfin" ? "border-info/25 bg-info/[0.06]" : "border-border bg-background/40",
      )}
    >
      <div className="text-muted-foreground flex items-center gap-1.5 text-xs font-semibold tracking-[0.16em] uppercase">
        <Icon className="h-3.5 w-3.5" />
        {label}
      </div>
      <div className="mt-1.5 flex items-center gap-2">
        <div className="min-w-0 flex-1 text-[15px] break-all">{children}</div>
        {copyValue ? (
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={t("web.settings.connectApps.copyField", { field: label })}
            onClick={handleCopy}
          >
            {copied ? <Check className="text-success h-4 w-4" /> : <Copy className="h-4 w-4" />}
          </Button>
        ) : null}
      </div>
      {hint ? <p className="text-muted-foreground mt-1 text-xs leading-relaxed">{hint}</p> : null}
    </div>
  );
}

function ScopeBanner({ kind, typedCredentials }: { kind: AppKind; typedCredentials: boolean }) {
  const t = useT();
  const isJellyfin = kind === "jellyfin";
  return (
    <div
      className={cn(
        "flex items-start gap-2.5 rounded-md border px-3.5 py-2.5",
        isJellyfin ? "border-info/30 bg-info/[0.07]" : "border-border bg-background/40",
      )}
    >
      {isJellyfin ? (
        <Cast className="text-info mt-0.5 h-4 w-4 shrink-0" />
      ) : (
        <MonitorSmartphone className="text-foreground mt-0.5 h-4 w-4 shrink-0" />
      )}
      <div className="min-w-0 space-y-0.5">
        <p className="text-sm font-medium">
          {isJellyfin
            ? t("web.settings.connectApps.scope.jellyfinTitle")
            : t("web.settings.connectApps.scope.siloTitle")}
        </p>
        <p className="text-muted-foreground text-xs leading-relaxed">
          {isJellyfin
            ? t("web.settings.connectApps.scope.jellyfinBody")
            : typedCredentials
              ? t("web.settings.connectApps.scope.siloBodyTyped")
              : t("web.settings.connectApps.scope.siloBody")}
        </p>
      </div>
    </div>
  );
}

function ProfilePicker({
  profiles,
  selectedId,
  onSelect,
}: {
  profiles: Profile[];
  selectedId: string | null;
  onSelect: (profile: Profile) => void;
}) {
  const t = useT();
  return (
    <div className="space-y-2">
      <p className="text-muted-foreground text-xs font-semibold tracking-[0.16em] uppercase">
        {t("web.settings.connectApps.profilePicker.title")}
      </p>
      <div className="flex flex-wrap gap-2">
        {profiles.map((profile) => {
          const active = profile.id === selectedId;
          return (
            <button
              key={profile.id}
              type="button"
              onClick={() => onSelect(profile)}
              aria-pressed={active}
              className={cn(
                "flex items-center gap-2 rounded-md border px-3 py-2 text-sm font-medium transition-colors",
                active
                  ? "border-info/50 bg-info/10 text-foreground"
                  : "border-border text-muted-foreground hover:text-foreground hover:bg-accent/60",
              )}
            >
              <Avatar className="h-6 w-6">
                {profile.avatar_url ? <AvatarImage src={profile.avatar_url} alt="" /> : null}
                <AvatarFallback className="text-xs font-semibold">
                  {profile.name.charAt(0).toUpperCase()}
                </AvatarFallback>
              </Avatar>
              {profile.name}
              {profile.has_pin ? <KeyRound className="text-muted-foreground h-3.5 w-3.5" /> : null}
            </button>
          );
        })}
      </div>
    </div>
  );
}

function TroubleshootingPanel({
  accountUsername,
  compatURL,
}: {
  accountUsername: string;
  compatURL: string | null;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);

  const rows = [
    {
      q: t("web.settings.connectApps.troubleshooting.noProfileQuestion"),
      a: t("web.settings.connectApps.troubleshooting.noProfileAnswer", {
        username: accountUsername,
      }),
    },
    {
      q: t("web.settings.connectApps.troubleshooting.pinQuestion"),
      a: t("web.settings.connectApps.troubleshooting.pinAnswer"),
    },
    {
      q: t("web.settings.connectApps.troubleshooting.hashPasswordQuestion"),
      a: t("web.settings.connectApps.troubleshooting.hashPasswordAnswer"),
    },
    {
      q: t("web.settings.connectApps.troubleshooting.duplicateQuestion"),
      a: t("web.settings.connectApps.troubleshooting.duplicateAnswer"),
    },
    ...(compatURL
      ? [
          {
            q: t("web.settings.connectApps.troubleshooting.siloAddressQuestion"),
            a: t("web.settings.connectApps.troubleshooting.siloAddressAnswer", { url: compatURL }),
          },
        ]
      : []),
  ];

  return (
    <section className="surface-panel rounded-md border px-4 py-4 shadow-none sm:px-5">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center gap-2 text-start"
        aria-expanded={open}
      >
        <TriangleAlert className="text-warning h-4 w-4" />
        <span className="text-sm font-semibold">
          {t("web.settings.connectApps.troubleshooting.title")}
        </span>
        <ChevronDown
          className={cn(
            "text-muted-foreground ms-auto h-4 w-4 transition-transform",
            open && "rotate-180",
          )}
        />
      </button>
      {open ? (
        <dl className="mt-3 space-y-3 text-sm">
          {rows.map((row) => (
            <div key={row.q} className="border-info/40 border-s-2 ps-3">
              <dt className="font-medium">{row.q}</dt>
              <dd className="text-muted-foreground mt-0.5 text-sm leading-relaxed break-words">
                {row.a}
              </dd>
            </div>
          ))}
        </dl>
      ) : null}
    </section>
  );
}

export default function ConnectAppsSettings() {
  const t = useT();
  const { user, profile: activeProfile } = useAuth();
  const {
    data: profiles = [],
    isLoading: profilesLoading,
    isError: profilesFailed,
  } = useProfiles();
  const {
    data: connectInfo,
    isLoading: connectInfoLoading,
    isError: connectInfoFailed,
  } = useCompatConnectInfo();

  const {
    data: authProviders,
    isLoading: authProvidersLoading,
    isError: authProvidersFailed,
  } = useAuthProviders();

  const [kind, setKind] = useState<AppKind>("silo");
  const [selectedProfileID, setSelectedProfileID] = useState<string | null>(null);

  // Default to the profile the user is already using — the one they're most
  // likely setting an app up for.
  const selectedProfile = useMemo<Profile | null>(() => {
    return (
      profiles.find((candidate) => candidate.id === selectedProfileID) ??
      profiles.find((candidate) => candidate.id === activeProfile?.id) ??
      profiles[0] ??
      null
    );
  }, [activeProfile?.id, profiles, selectedProfileID]);

  const accountUsername = user?.username ?? "";
  const siloURL = typeof window === "undefined" ? "" : window.location.origin;
  const compatEnabled = connectInfo?.jellyfin.enabled ?? false;
  const compatPendingRestart = connectInfo?.jellyfin.pending_restart ?? false;
  const compatURL = connectInfo?.jellyfin.public_url?.trim() || null;
  const compatURLIsLoopback = compatURL !== null && isLoopbackURL(compatURL);
  // Absent field (older server) means the account can use a password.
  const passwordLoginAvailable = connectInfo?.account?.password_login_available ?? true;
  // A directory account's Jellyfin sign-in goes to the directory, under its
  // name there.
  const directoryLoginAvailable = connectInfo?.account?.directory_login_available ?? false;
  const jellyfinSignInAvailable = passwordLoginAvailable || directoryLoginAvailable;
  const jellyfinAccountName =
    (directoryLoginAvailable && connectInfo?.account?.directory_username?.trim()) ||
    accountUsername;
  const passwordFormShown = authProviders?.password_login ?? true;
  // Discovery lists the local provider only while the server allows password
  // sign-in, which tells a switched-off server apart from a provider account.
  const serverPasswordSignInOff =
    authProviders !== undefined && !authProviders.items.some((entry) => entry.id === "local");
  // Linked identities matter only when the password form isn't the answer.
  const needsIdentities =
    connectInfo !== undefined &&
    authProviders !== undefined &&
    !(passwordLoginAvailable && passwordFormShown);
  const {
    data: identities,
    isLoading: identitiesLoading,
    isError: identitiesFailed,
  } = useAccountIdentities(needsIdentities);
  const siloSignIn = resolveSiloSignIn(
    passwordLoginAvailable,
    passwordFormShown,
    authProviders?.items ?? [],
    identities?.items ?? [],
  );
  const isJellyfin = kind === "jellyfin";
  // The Silo tab also needs the server's sign-in options; the Jellyfin tab
  // doesn't, so their failures stay out of it.
  const isLoading =
    profilesLoading ||
    connectInfoLoading ||
    (!isJellyfin && (authProvidersLoading || (needsIdentities && identitiesLoading)));
  // A failed load must not read as "compat is switched off", and an empty
  // profile list must not read as "your account name alone is enough".
  const loadFailed =
    connectInfoFailed ||
    profilesFailed ||
    (!isJellyfin && (authProvidersFailed || (needsIdentities && identitiesFailed)));
  // Everything below the credential panel is only meaningful when we actually
  // showed credentials above it.
  const showCompatCredentials =
    isJellyfin && !isLoading && !loadFailed && compatEnabled && jellyfinSignInAvailable;

  const jellyfinUsername = selectedProfile
    ? buildJellyfinUsername(jellyfinAccountName, selectedProfile.name)
    : "";
  const usernameIssue = selectedProfile ? jellyfinUsernameIssue(selectedProfile.name) : null;
  // A directory account signs in with its directory username, which can
  // differ from the Silo account name.
  const siloUsername =
    siloSignIn.kind === "directory" && siloSignIn.username.trim() !== ""
      ? siloSignIn.username
      : accountUsername;

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.connectApps.title")}
        subtitle={t("web.settings.connectApps.subtitle")}
      />

      <div
        className="surface-panel-subtle grid grid-cols-1 gap-1 rounded-[1.1rem] p-1 sm:grid-cols-2"
        role="group"
        aria-label={t("web.settings.connectApps.appTypeLabel")}
      >
        {(
          [
            {
              id: "silo",
              label: t("web.settings.connectApps.appType.silo"),
              icon: MonitorSmartphone,
            },
            { id: "jellyfin", label: t("web.settings.connectApps.appType.jellyfin"), icon: Cast },
          ] as const
        ).map((option) => {
          const active = option.id === kind;
          const Icon = option.icon;
          return (
            <button
              key={option.id}
              type="button"
              onClick={() => setKind(option.id)}
              aria-pressed={active}
              className={cn(
                "flex items-center justify-center gap-2 rounded-[0.85rem] px-3 py-2.5 text-sm font-medium transition-colors",
                active
                  ? option.id === "jellyfin"
                    ? "bg-info/15 text-info ring-info/30 ring-1"
                    : "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              <Icon className="h-4 w-4" />
              {option.label}
            </button>
          );
        })}
      </div>

      <section
        className={cn(
          "surface-panel space-y-4 rounded-md border px-4 py-5 shadow-none sm:px-5",
          isJellyfin && "border-info/35",
        )}
      >
        <ScopeBanner
          kind={kind}
          typedCredentials={
            isJellyfin || siloSignIn.kind === "password" || siloSignIn.kind === "directory"
          }
        />

        {isLoading ? (
          <div className="space-y-2.5">
            {Array.from({ length: 3 }).map((_, index) => (
              <Skeleton key={index} className="h-[76px] w-full rounded-md" />
            ))}
          </div>
        ) : loadFailed ? (
          <div className="border-destructive/40 rounded-md border border-dashed px-3.5 py-4">
            <p className="text-sm font-medium">{t("web.settings.connectApps.loadFailed.title")}</p>
            <p className="text-muted-foreground mt-1 text-sm leading-relaxed">
              {t("web.settings.connectApps.loadFailed.body")}
            </p>
          </div>
        ) : isJellyfin && !jellyfinSignInAvailable ? (
          <div className="border-border rounded-md border border-dashed px-3.5 py-4">
            <p className="text-sm font-medium">
              {t("web.settings.connectApps.noJellyfinSignIn.title")}
            </p>
            <p className="text-muted-foreground mt-1 text-sm leading-relaxed">
              {serverPasswordSignInOff
                ? t("web.settings.connectApps.noJellyfinSignIn.passwordSignInOff")
                : t("web.settings.connectApps.noJellyfinSignIn.externalProvider")}
            </p>
          </div>
        ) : isJellyfin && !compatEnabled ? (
          <div className="border-border rounded-md border border-dashed px-3.5 py-4">
            <p className="text-sm font-medium">
              {compatPendingRestart
                ? t("web.settings.connectApps.compatOff.pendingRestartTitle")
                : t("web.settings.connectApps.compatOff.title")}
            </p>
            <p className="text-muted-foreground mt-1 text-sm leading-relaxed">
              {compatPendingRestart
                ? t("web.settings.connectApps.compatOff.pendingRestartBody")
                : t("web.settings.connectApps.compatOff.body")}
            </p>
          </div>
        ) : !isJellyfin && siloSignIn.kind === "none" ? (
          <div className="border-border rounded-md border border-dashed px-3.5 py-4">
            <p className="text-sm font-medium">
              {passwordLoginAvailable
                ? t("web.settings.connectApps.noSiloSignIn.recoveryTitle")
                : t("web.settings.connectApps.noSiloSignIn.title")}
            </p>
            <p className="text-muted-foreground mt-1 text-sm leading-relaxed">
              {passwordLoginAvailable
                ? t("web.settings.connectApps.noSiloSignIn.recoveryBody", {
                    login_url: `${siloURL}/login?local=1`,
                    activate_url: `${siloURL}/activate`,
                    url: (chunks) => (
                      <span className="text-foreground font-mono break-all">{chunks}</span>
                    ),
                  })
                : t("web.settings.connectApps.noSiloSignIn.body")}
            </p>
          </div>
        ) : !isJellyfin && siloSignIn.kind === "network" ? (
          <>
            <div className="space-y-2.5">
              <FieldRow
                label={t("web.settings.connectApps.field.server")}
                icon={Server}
                kind={kind}
                copyValue={siloURL}
              >
                <code className="font-mono">{siloURL}</code>
              </FieldRow>

              <FieldRow
                label={t("web.settings.connectApps.field.signInWith")}
                icon={LogIn}
                kind={kind}
                hint={t("web.settings.connectApps.network.hint", {
                  provider: siloSignIn.providerName,
                })}
              >
                <span className="font-medium">{siloSignIn.providerName}</span>
              </FieldRow>
            </div>

            <div className="border-border/70 rounded-md border border-dashed px-3.5 py-2.5">
              <p className="text-muted-foreground text-xs leading-relaxed">
                {t("web.settings.connectApps.profilePickerNote")}
              </p>
            </div>
          </>
        ) : !isJellyfin && siloSignIn.kind === "provider" ? (
          <>
            <div className="space-y-2.5">
              <FieldRow
                label={t("web.settings.connectApps.field.server")}
                icon={Server}
                kind={kind}
                copyValue={siloURL}
              >
                <code className="font-mono">{siloURL}</code>
              </FieldRow>

              <FieldRow
                label={t("web.settings.connectApps.field.signInWith")}
                icon={LogIn}
                kind={kind}
                hint={
                  siloSignIn.provider.native_start_path
                    ? t("web.settings.connectApps.provider.hint", {
                        provider: siloSignIn.provider.display_name,
                      })
                    : t("web.settings.connectApps.provider.noPublicAddress", {
                        provider: siloSignIn.provider.display_name,
                      })
                }
              >
                <span className="font-medium">{siloSignIn.provider.display_name}</span>
              </FieldRow>
            </div>

            <div className="border-border/70 space-y-2 rounded-md border border-dashed px-3.5 py-2.5">
              <p className="text-muted-foreground flex items-start gap-1.5 text-xs leading-relaxed">
                <Tv className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span>
                  {t("web.settings.connectApps.provider.tvCode", {
                    activate_url: `${siloURL}/activate`,
                    url: (chunks) => (
                      <span className="text-foreground font-mono break-all">{chunks}</span>
                    ),
                  })}
                </span>
              </p>
              <p className="text-muted-foreground text-xs leading-relaxed">
                {t("web.settings.connectApps.profilePickerNote")}
              </p>
            </div>
          </>
        ) : (
          <>
            {isJellyfin && profiles.length > 0 ? (
              <ProfilePicker
                profiles={profiles}
                selectedId={selectedProfile?.id ?? null}
                onSelect={(profile) => setSelectedProfileID(profile.id)}
              />
            ) : null}

            <div className="space-y-2.5">
              <FieldRow
                label={t("web.settings.connectApps.field.server")}
                icon={Server}
                kind={kind}
                copyValue={
                  isJellyfin
                    ? compatURLIsLoopback
                      ? undefined
                      : (compatURL ?? undefined)
                    : siloURL
                }
                hint={
                  isJellyfin
                    ? compatURLIsLoopback
                      ? t("web.settings.connectApps.server.loopbackHint")
                      : t("web.settings.connectApps.server.compatHint")
                    : undefined
                }
              >
                {isJellyfin ? (
                  compatURL ? (
                    <code
                      className={cn("font-mono", compatURLIsLoopback && "text-muted-foreground")}
                    >
                      {compatURL}
                    </code>
                  ) : (
                    <span className="text-muted-foreground text-sm">
                      {t("web.settings.connectApps.server.noPublicAddress")}
                    </span>
                  )
                ) : (
                  <code className="font-mono">{siloURL}</code>
                )}
              </FieldRow>

              <FieldRow
                label={t("web.settings.connectApps.field.username")}
                icon={Users}
                kind={kind}
                copyValue={
                  isJellyfin
                    ? usernameIssue
                      ? undefined
                      : jellyfinUsername || undefined
                    : siloUsername || undefined
                }
                hint={
                  isJellyfin
                    ? (usernameIssue ??
                      t("web.settings.connectApps.username.jellyfinHint", {
                        username: jellyfinAccountName,
                      }))
                    : siloSignIn.kind === "directory"
                      ? t("web.settings.connectApps.username.directoryHint", {
                          provider: siloSignIn.provider.display_name,
                        })
                      : t("web.settings.connectApps.username.siloHint")
                }
              >
                {isJellyfin && selectedProfile ? (
                  <HashString before={jellyfinAccountName} after={selectedProfile.name} />
                ) : (
                  <code className="font-mono">
                    {isJellyfin ? jellyfinAccountName : siloUsername}
                  </code>
                )}
              </FieldRow>

              <FieldRow
                label={t("web.settings.connectApps.field.password")}
                icon={KeyRound}
                kind={kind}
                hint={
                  isJellyfin
                    ? buildJellyfinPasswordHint(selectedProfile)
                    : siloSignIn.kind === "directory"
                      ? t("web.settings.connectApps.password.directoryHint", {
                          provider: siloSignIn.provider.display_name,
                        })
                      : t("web.settings.connectApps.password.siloHint")
                }
              >
                {isJellyfin && selectedProfile?.has_pin ? (
                  <HashString
                    before={t("web.settings.connectApps.password.placeholder")}
                    after={t("web.settings.connectApps.password.pin")}
                  />
                ) : (
                  <code className="font-mono">
                    {t("web.settings.connectApps.password.placeholder")}
                  </code>
                )}
              </FieldRow>
            </div>

            {isJellyfin ? (
              <p className="text-muted-foreground flex items-start gap-1.5 text-xs leading-relaxed">
                <X className="text-info mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span>{t("web.settings.connectApps.jellyfinFormatNote")}</span>
              </p>
            ) : (
              <div className="border-border/70 rounded-md border border-dashed px-3.5 py-2.5">
                <p className="text-muted-foreground text-xs leading-relaxed">
                  {t("web.settings.connectApps.profilePickerNote")}
                </p>
              </div>
            )}
          </>
        )}
      </section>

      {showCompatCredentials ? (
        <TroubleshootingPanel accountUsername={jellyfinAccountName} compatURL={compatURL} />
      ) : null}

      {showCompatCredentials && profiles.length > 1 ? (
        <section className="surface-panel rounded-md border px-4 py-4 shadow-none sm:px-5">
          <h3 className="text-sm font-semibold">{t("web.settings.connectApps.glance.title")}</h3>
          <ul className="mt-2.5 space-y-1.5">
            {profiles.map((profile) => {
              // Same guard as the Username field: a name containing # yields a
              // username the resolver can't parse, so don't list one here either.
              const issue = jellyfinUsernameIssue(profile.name);
              return (
                <li key={profile.id} className="flex flex-wrap items-center gap-2 text-sm">
                  {issue ? (
                    <>
                      <span className="text-muted-foreground font-mono line-through">
                        {profile.name}
                      </span>
                      <Badge variant="outline" className="text-muted-foreground">
                        {t("web.settings.connectApps.glance.rename")}
                      </Badge>
                    </>
                  ) : (
                    <>
                      <HashString before={jellyfinAccountName} after={profile.name} />
                      {profile.has_pin ? (
                        <Badge variant="outline" className="text-muted-foreground">
                          {t("web.settings.connectApps.glance.needsPin")}
                        </Badge>
                      ) : null}
                    </>
                  )}
                </li>
              );
            })}
          </ul>
        </section>
      ) : null}
    </div>
  );
}
