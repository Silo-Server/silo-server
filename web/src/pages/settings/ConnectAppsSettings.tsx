import { useEffect, useMemo, useState } from "react";
import {
  Cast,
  Check,
  ChevronDown,
  Copy,
  KeyRound,
  MonitorSmartphone,
  Server,
  TriangleAlert,
  Users,
  X,
} from "lucide-react";
import { toast } from "sonner";

import type { Profile } from "@/api/types";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/hooks/useAuth";
import { useCompatConnectInfo } from "@/hooks/queries/compat";
import { useProfiles } from "@/hooks/queries/profiles";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";
import {
  buildJellyfinPasswordHint,
  buildJellyfinUsername,
  isLoopbackURL,
  jellyfinUsernameIssue,
  JELLYFIN_APP_EXAMPLES,
  SILO_APP_EXAMPLES,
} from "./connectApps";

type AppKind = "silo" | "jellyfin";

/** Renders `name#Profile` with the separator tinted so `#` reads as structure. */
function HashString({ before, after }: { before: string; after: string }) {
  return (
    <span className="font-mono">
      {before}
      <span className="text-info font-semibold">#</span>
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
      toast.error("Couldn't copy — select the text and copy it manually");
    }
  }

  return (
    <div
      className={cn(
        "rounded-md border px-3.5 py-2.5",
        kind === "jellyfin" ? "border-info/25 bg-info/[0.06]" : "border-border bg-background/40",
      )}
    >
      <div className="text-muted-foreground flex items-center gap-1.5 text-[10px] font-semibold tracking-[0.16em] uppercase">
        <Icon className="h-3.5 w-3.5" />
        {label}
      </div>
      <div className="mt-1.5 flex items-center gap-2">
        <div className="min-w-0 flex-1 text-[15px] break-all">{children}</div>
        {copyValue ? (
          <Button size="icon-sm" variant="ghost" aria-label={`Copy ${label}`} onClick={handleCopy}>
            {copied ? <Check className="text-success h-4 w-4" /> : <Copy className="h-4 w-4" />}
          </Button>
        ) : null}
      </div>
      {hint ? <p className="text-muted-foreground mt-1 text-xs leading-relaxed">{hint}</p> : null}
    </div>
  );
}

function ScopeBanner({ kind }: { kind: AppKind }) {
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
          {isJellyfin ? "For Jellyfin-compatible apps only" : "For Silo's own apps"}
        </p>
        <p className="text-muted-foreground text-xs leading-relaxed">
          {isJellyfin ? JELLYFIN_APP_EXAMPLES : SILO_APP_EXAMPLES}.{" "}
          {isJellyfin
            ? "These credentials will not work on a Silo sign-in screen."
            : "Don't add a # to either field here."}
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
  return (
    <div className="space-y-2">
      <p className="text-muted-foreground text-[10px] font-semibold tracking-[0.16em] uppercase">
        Which profile are you signing in as?
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
                <AvatarFallback className="text-[10px] font-semibold">
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
  const [open, setOpen] = useState(false);

  const rows = [
    {
      q: "You left the profile off the username",
      a: `Signing in as plain "${accountUsername}" only works if a profile is named "${accountUsername}", or exactly one profile has no PIN. Adding #ProfileName always works.`,
    },
    {
      q: "The profile has a PIN and you didn't append it",
      a: "PIN-protected profiles need password#PIN. There is no second prompt — a Jellyfin app never asks.",
    },
    {
      q: "Your account password itself contains a #",
      a: "Type it in full and append #PIN anyway. Silo splits at the last # only.",
    },
    {
      q: "Two profiles share a name",
      a: "Profile names are matched without case sensitivity, so duplicates are ambiguous. Rename one in Settings → Profiles.",
    },
    ...(compatURL
      ? [
          {
            q: "You used the Silo app's address",
            a: `The compatibility API is a separate address: ${compatURL}`,
          },
        ]
      : []),
  ];

  return (
    <section className="surface-panel rounded-md border px-4 py-4 shadow-none sm:px-5">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center gap-2 text-left"
        aria-expanded={open}
      >
        <TriangleAlert className="text-warning h-4 w-4" />
        <span className="text-sm font-semibold">
          A Jellyfin app says my username or password is wrong
        </span>
        <ChevronDown
          className={cn(
            "text-muted-foreground ml-auto h-4 w-4 transition-transform",
            open && "rotate-180",
          )}
        />
      </button>
      {open ? (
        <dl className="mt-3 space-y-3 text-sm">
          {rows.map((row) => (
            <div key={row.q} className="border-info/40 border-l-2 pl-3">
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

  const [kind, setKind] = useState<AppKind>("jellyfin");
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
  const isJellyfin = kind === "jellyfin";
  const isLoading = profilesLoading || connectInfoLoading;
  // A failed load must not read as "compat is switched off", and an empty
  // profile list must not read as "your account name alone is enough".
  const loadFailed = connectInfoFailed || profilesFailed;
  // Everything below the credential panel is only meaningful when we actually
  // showed credentials above it.
  const showCompatCredentials =
    isJellyfin && !isLoading && !loadFailed && compatEnabled && passwordLoginAvailable;

  const jellyfinUsername = selectedProfile
    ? buildJellyfinUsername(accountUsername, selectedProfile.name)
    : "";
  const usernameIssue = selectedProfile ? jellyfinUsernameIssue(selectedProfile.name) : null;

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Connect Apps</h2>
        <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
          Exactly what to type on a sign-in screen. Pick the kind of app you're using — the
          credentials are different for each.
        </p>
      </div>

      <div
        className="surface-panel-subtle grid grid-cols-1 gap-1 rounded-[1.1rem] p-1 sm:grid-cols-2"
        role="group"
        aria-label="App type"
      >
        {(
          [
            { id: "silo", label: "Silo app or website", icon: MonitorSmartphone },
            { id: "jellyfin", label: "Jellyfin-compatible app", icon: Cast },
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
        <ScopeBanner kind={kind} />

        {isLoading ? (
          <div className="space-y-2.5">
            {Array.from({ length: 3 }).map((_, index) => (
              <Skeleton key={index} className="h-[76px] w-full rounded-md" />
            ))}
          </div>
        ) : loadFailed ? (
          <div className="border-destructive/40 rounded-md border border-dashed px-3.5 py-4">
            <p className="text-sm font-medium">Couldn't load your sign-in details</p>
            <p className="text-muted-foreground mt-1 text-sm leading-relaxed">
              Reload the page to try again. Credentials are withheld rather than guessed, so nothing
              here is stale or wrong.
            </p>
          </div>
        ) : isJellyfin && !passwordLoginAvailable ? (
          <div className="border-border rounded-md border border-dashed px-3.5 py-4">
            <p className="text-sm font-medium">This account can't sign in to a Jellyfin app</p>
            <p className="text-muted-foreground mt-1 text-sm leading-relaxed">
              It signs in through an external provider rather than a Silo password, and the
              compatibility API only accepts Silo passwords. Use a Silo app, or ask an administrator
              about an account with password sign-in.
            </p>
          </div>
        ) : isJellyfin && !compatEnabled ? (
          <div className="border-border rounded-md border border-dashed px-3.5 py-4">
            <p className="text-sm font-medium">
              {compatPendingRestart
                ? "The Jellyfin compatibility API isn't running yet"
                : "The Jellyfin compatibility API is turned off"}
            </p>
            <p className="text-muted-foreground mt-1 text-sm leading-relaxed">
              {compatPendingRestart
                ? "An administrator has turned it on, but the server has to restart before it starts accepting connections."
                : "Third-party Jellyfin apps can't reach this server until an administrator enables it in Admin → Settings → Compatibility."}
            </p>
          </div>
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
                label="Server"
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
                      ? "This address only works on the server itself, so phones and TVs can't reach it. An administrator needs to set the compatibility API's public address."
                      : "The compatibility API listens on its own address — not the one this page is on."
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
                      No public address configured — ask an administrator.
                    </span>
                  )
                ) : (
                  <code className="font-mono">{siloURL}</code>
                )}
              </FieldRow>

              <FieldRow
                label="Username"
                icon={Users}
                kind={kind}
                copyValue={
                  isJellyfin
                    ? usernameIssue
                      ? undefined
                      : jellyfinUsername || undefined
                    : accountUsername || undefined
                }
                hint={
                  isJellyfin
                    ? (usernameIssue ??
                      `Your account name, then #, then the profile name — not just "${accountUsername}".`)
                    : "Just your account name."
                }
              >
                {isJellyfin && selectedProfile ? (
                  <HashString before={accountUsername} after={selectedProfile.name} />
                ) : (
                  <code className="font-mono">{accountUsername}</code>
                )}
              </FieldRow>

              <FieldRow
                label="Password"
                icon={KeyRound}
                kind={kind}
                hint={
                  isJellyfin
                    ? buildJellyfinPasswordHint(selectedProfile)
                    : "Your account password. The profile PIN is asked for separately, in the app."
                }
              >
                {isJellyfin && selectedProfile?.has_pin ? (
                  <HashString before="your password" after="PIN" />
                ) : (
                  <code className="font-mono">your password</code>
                )}
              </FieldRow>
            </div>

            {isJellyfin ? (
              <p className="text-muted-foreground flex items-start gap-1.5 text-xs leading-relaxed">
                <X className="text-info mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span>
                  Jellyfin apps offer only two boxes and never prompt for a profile, so the profile
                  name and PIN are appended here. This format is rejected on a Silo sign-in screen.
                </span>
              </p>
            ) : (
              <div className="border-border/70 rounded-md border border-dashed px-3.5 py-2.5">
                <p className="text-muted-foreground text-xs leading-relaxed">
                  After signing in you'll choose a profile from the profile picker, and
                  PIN-protected profiles prompt for their PIN there.
                </p>
              </div>
            )}
          </>
        )}
      </section>

      {showCompatCredentials ? (
        <TroubleshootingPanel accountUsername={accountUsername} compatURL={compatURL} />
      ) : null}

      {showCompatCredentials && profiles.length > 1 ? (
        <section className="surface-panel rounded-md border px-4 py-4 shadow-none sm:px-5">
          <h3 className="text-sm font-semibold">Every profile at a glance</h3>
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
                        rename to use from a Jellyfin app
                      </Badge>
                    </>
                  ) : (
                    <>
                      <HashString before={accountUsername} after={profile.name} />
                      {profile.has_pin ? (
                        <Badge variant="outline" className="text-muted-foreground">
                          needs #PIN
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
