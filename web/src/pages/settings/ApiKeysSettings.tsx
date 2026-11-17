import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { Copy, Plus } from "lucide-react";
import { toast } from "sonner";

import type { Profile } from "@/api/types";
import type { PersonalAPIKey, PersonalAPIKeyScopes } from "@/api/v2/personalApiKeys";
import { V2ProblemError } from "@/api/v2/request";
import { PageHeader } from "@/components/page/PageHeader";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
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
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useCreatePersonalApiKey,
  usePersonalApiKeys,
  usePersonalApiKeyScopes,
  useRevokePersonalApiKey,
} from "@/hooks/queries/personalApiKeys";
import { useProfiles } from "@/hooks/queries/profiles";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useT, type MessageKey } from "@/i18n";
import { copyTextToClipboard } from "@/lib/clipboard";
import { formatDate } from "@/lib/datetime";

type Scope = PersonalAPIKeyScopes["scopes"][number];

// What each user scope allows, in the viewer's language. A scope the server
// adds later shows the server's own English description until it is listed.
const SCOPE_DESCRIPTIONS: Partial<Record<string, MessageKey>> = {
  "libraries:read": "web.settings.apiKeys.scope.librariesRead",
  "catalog:read": "web.settings.apiKeys.scope.catalogRead",
  "watch:read": "web.settings.apiKeys.scope.watchRead",
  "watch:write": "web.settings.apiKeys.scope.watchWrite",
  requests: "web.settings.apiKeys.scope.requests",
  notifications: "web.settings.apiKeys.scope.notifications",
};

// The select's value for a key that is not tied to one profile.
const WHOLE_ACCOUNT = "account";

/**
 * Settings → API Keys: the keys the acting profile may see, and creating and
 * revoking them. The primary profile sees every key on the account and may
 * create one for the whole account or for any profile; any other profile sees
 * and creates only its own. Server admins also create unscoped keys, but on
 * the admin API Keys page; this page only offers the user scopes.
 */
export default function ApiKeysSettings() {
  const t = useT();
  const { user } = useAuth();
  const { profile } = useCurrentProfile();
  const profiles = useProfiles();
  const capability = usePersonalApiKeyScopes();
  const keys = usePersonalApiKeys(profile?.id ?? "");
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<PersonalAPIKey | null>(null);

  if (capability.isLoading || keys.isLoading) {
    return <div className="text-muted-foreground pt-4">{t("web.settings.apiKeys.loading")}</div>;
  }

  const rows = keys.data?.pages.flatMap((page) => page.items) ?? [];
  const userScopes = (capability.data?.scopes ?? []).filter((scope) => scope.audience === "user");
  const enabled = capability.data?.available === true && capability.data.user_keys_enabled;
  // Creating needs a loaded list: the list refetches after a create, and a
  // list with nothing cached would show its loading state in place of the
  // dialog that holds the new key.
  const listLoaded = keys.data !== undefined;
  const maxKeys = capability.data?.max_user_keys ?? 0;
  // The primary profile sees the whole account's keys, so its count is the
  // account's; another profile learns the limit from the server's refusal.
  // An admin's own account keys are not held to the limit, so the server
  // decides for an admin.
  const atLimit =
    user?.role !== "admin" &&
    profile?.is_primary === true &&
    !keys.hasNextPage &&
    maxKeys > 0 &&
    rows.length >= maxKeys;
  const profileName = (id: string) => profiles.data.find((p) => p.id === id)?.name ?? id;

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.apiKeys.title")}
        subtitle={t("web.settings.apiKeys.subtitle")}
        actions={
          enabled && listLoaded && !atLimit && profile ? (
            <Button onClick={() => setCreating(true)}>
              <Plus data-icon="inline-start" />
              {t("web.settings.apiKeys.createKey")}
            </Button>
          ) : null
        }
      />
      {capability.isError ? (
        <p role="alert" className="text-destructive text-sm">
          {t("web.settings.apiKeys.loadFailed")}
        </p>
      ) : !enabled && user?.role !== "admin" ? (
        <p className="text-muted-foreground text-sm" role="status">
          {t("web.settings.apiKeys.disabled")}
        </p>
      ) : null}
      {enabled && atLimit ? (
        <p className="text-muted-foreground text-sm" role="status">
          {t("web.settings.apiKeys.limitReached", { max: maxKeys })}
        </p>
      ) : null}
      <SettingsGroup title={t("web.settings.apiKeys.list.heading")}>
        {keys.isError ? (
          <p role="alert" className="text-destructive text-sm">
            {t("web.settings.apiKeys.loadFailed")}
          </p>
        ) : rows.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t("web.settings.apiKeys.empty")}</p>
        ) : (
          <ul className="divide-border divide-y">
            {rows.map((key) => (
              <li key={key.id} className="flex flex-wrap items-start justify-between gap-3 py-3">
                <div className="min-w-0 space-y-1">
                  <p className="font-medium break-words">{key.label}</p>
                  <p className="text-muted-foreground text-sm">
                    {key.profile_id
                      ? t("web.settings.apiKeys.list.profile", {
                          profile: profileName(key.profile_id),
                        })
                      : t("web.settings.apiKeys.list.wholeAccount")}
                  </p>
                  <p className="text-muted-foreground font-mono text-xs break-all">
                    {key.scopes.join(" ")}
                  </p>
                  <p className="text-muted-foreground flex flex-wrap gap-x-3 text-xs">
                    <span>
                      {t("web.settings.apiKeys.list.created", {
                        date: formatDate(key.created_at, "medium"),
                      })}
                    </span>
                    <span>
                      {key.last_used_at
                        ? t("web.settings.apiKeys.list.lastUsed", {
                            date: formatDate(key.last_used_at, "medium"),
                          })
                        : t("web.settings.apiKeys.list.neverUsed")}
                    </span>
                  </p>
                </div>
                <Button
                  variant="outline"
                  size="sm"
                  aria-label={t("web.settings.apiKeys.list.revokeNamed", { label: key.label })}
                  onClick={() => setRevoking(key)}
                >
                  {t("web.settings.apiKeys.list.revoke")}
                </Button>
              </li>
            ))}
          </ul>
        )}
        {keys.hasNextPage ? (
          <Button
            variant="outline"
            disabled={keys.isFetchingNextPage}
            onClick={() => void keys.fetchNextPage()}
          >
            {t("web.settings.apiKeys.list.loadMore")}
          </Button>
        ) : null}
      </SettingsGroup>
      {creating && profile ? (
        <CreateKeyDialog
          profile={profile}
          profiles={profiles.data}
          scopes={userScopes}
          onClose={() => setCreating(false)}
        />
      ) : null}
      {revoking && profile ? (
        <RevokeKeyDialog
          apiKey={revoking}
          profileId={profile.id}
          onClose={() => setRevoking(null)}
        />
      ) : null}
    </div>
  );
}

function CreateKeyDialog({
  profile,
  profiles,
  scopes,
  onClose,
}: {
  profile: Profile;
  profiles: Profile[];
  scopes: Scope[];
  onClose: () => void;
}) {
  const t = useT();
  const create = useCreatePersonalApiKey(profile.id);
  const [label, setLabel] = useState("");
  // Only the primary profile may choose; every other profile's key is its own.
  const [target, setTarget] = useState(profile.is_primary ? WHOLE_ACCOUNT : profile.id);
  const [chosen, setChosen] = useState<string[]>([]);
  const [secret, setSecret] = useState<string | null>(null);
  const [error, setError] = useState("");
  const lock = useRef(false);
  // What the key shown was created for: the form's choice can still change
  // while the request runs, and the usage text must describe the key made.
  const [createdFor, setCreatedFor] = useState(target);
  // The key's kind comes from that choice, not from the profile list, which
  // may not have loaded; then the profile is named by what is known.
  const boundProfileName =
    profiles.find((p) => p.id === createdFor)?.name ??
    (profile.id === createdFor ? profile.name : createdFor);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (lock.current || chosen.length === 0) return;
    lock.current = true;
    setError("");
    try {
      const submitted = target;
      const created = await create.mutateAsync({
        label,
        scopes: chosen,
        profileId: submitted === WHOLE_ACCOUNT ? undefined : submitted,
      });
      setCreatedFor(submitted);
      setSecret(created.key);
    } catch (e) {
      // A refusal says what was wrong; anything else may still have made a key.
      setError(
        e instanceof V2ProblemError && e.status < 500
          ? e.message
          : t("web.settings.apiKeys.create.uncertain"),
      );
    } finally {
      lock.current = false;
    }
  }

  async function copy() {
    // The key is shown once, so a failed copy leaves the dialog open.
    try {
      await copyTextToClipboard(secret!);
    } catch {
      setError(t("web.settings.apiKeys.create.copyFailed"));
      return;
    }
    toast.success(t("web.settings.apiKeys.create.copied"));
    onClose();
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !create.isPending) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("web.settings.apiKeys.create.title")}</DialogTitle>
          <DialogDescription>{t("web.settings.apiKeys.create.description")}</DialogDescription>
        </DialogHeader>
        {secret ? (
          <div className="flex flex-col gap-4">
            <p>{t("web.settings.apiKeys.create.secretNotice")}</p>
            <code className="break-all">{secret}</code>
            <KeyUsage
              secret={secret}
              boundTo={createdFor === WHOLE_ACCOUNT ? undefined : boundProfileName}
              example={profile}
            />
            {error ? <p role="alert">{error}</p> : null}
            <Button onClick={() => void copy()}>
              <Copy data-icon="inline-start" />
              {t("web.settings.apiKeys.create.copyAndClose")}
            </Button>
          </div>
        ) : (
          <form onSubmit={(event) => void submit(event)} className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="api-key-label">{t("web.settings.apiKeys.create.label")}</Label>
              <Input
                id="api-key-label"
                value={label}
                onChange={(event) => setLabel(event.target.value)}
                required
              />
            </div>
            {profile.is_primary ? (
              <div className="flex flex-col gap-2">
                <Label htmlFor="api-key-target">{t("web.settings.apiKeys.create.actsAs")}</Label>
                <Select value={target} onValueChange={setTarget}>
                  <SelectTrigger id="api-key-target">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value={WHOLE_ACCOUNT}>
                        {t("web.settings.apiKeys.list.wholeAccount")}
                      </SelectItem>
                      {profiles.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-sm">
                  {t("web.settings.apiKeys.create.actsAsHint")}
                </p>
              </div>
            ) : null}
            <fieldset className="flex min-w-0 flex-col gap-3">
              <legend className="mb-2 text-sm font-medium">
                {t("web.settings.apiKeys.create.access")}
              </legend>
              {scopes.map((scope) => {
                const id = `api-key-scope-${scope.name}`;
                return (
                  <div key={scope.name} className="flex items-start gap-3">
                    <Checkbox
                      id={id}
                      className="mt-0.5"
                      checked={chosen.includes(scope.name)}
                      onCheckedChange={(value) =>
                        setChosen((current) =>
                          value === true
                            ? [...current, scope.name]
                            : current.filter((name) => name !== scope.name),
                        )
                      }
                      aria-describedby={`${id}-description`}
                    />
                    <div className="flex min-w-0 flex-col gap-0.5">
                      <Label htmlFor={id} className="font-mono text-sm break-all">
                        {scope.name}
                      </Label>
                      <p id={`${id}-description`} className="text-muted-foreground text-sm">
                        {SCOPE_DESCRIPTIONS[scope.name]
                          ? t(SCOPE_DESCRIPTIONS[scope.name]!)
                          : scope.description}
                      </p>
                    </div>
                  </div>
                );
              })}
              <p className="text-muted-foreground text-sm">
                {t("web.settings.apiKeys.create.accessHint")}
              </p>
            </fieldset>
            {error ? <p role="alert">{error}</p> : null}
            <DialogFooter>
              <Button type="button" variant="outline" disabled={create.isPending} onClick={onClose}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" disabled={create.isPending || chosen.length === 0}>
                {create.isPending
                  ? t("web.settings.apiKeys.create.creating")
                  : t("web.settings.apiKeys.create.submit")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

// Request text shown as code: header names and a path, the same in every language.
const API_DOCS_PATH = "/api/v2/docs";
const PROFILES_REQUEST = "GET /api/v2/profiles";

/**
 * How to send the key just created. A key tied to one profile needs nothing
 * else; a whole-account key also names the profile it acts as, shown with
 * the acting profile's ID as the example.
 */
function KeyUsage({
  secret,
  boundTo,
  example,
}: {
  secret: string;
  /** The name of the one profile the key is tied to; undefined for a whole-account key. */
  boundTo: string | undefined;
  example: Profile;
}) {
  const t = useT();
  // Enough of the key to recognize it; the full key is shown once, above.
  const authorization = `Authorization: Bearer ${secret.slice(0, 11)}…`;
  const profileHeader = `X-Profile-Id: ${example.id}`;
  return (
    <section className="border-border flex flex-col gap-2 border-t pt-4 text-sm">
      <h3 className="font-medium">{t("web.settings.apiKeys.usage.title")}</h3>
      <p className="text-muted-foreground">{t("web.settings.apiKeys.usage.header")}</p>
      <code className="break-all">{authorization}</code>
      {boundTo ? (
        <p className="text-muted-foreground">
          {t("web.settings.apiKeys.usage.profileKey", { profile: boundTo })}
        </p>
      ) : (
        <>
          <p className="text-muted-foreground">
            {t("web.settings.apiKeys.usage.accountKey", {
              profile: example.name,
              endpoint: PROFILES_REQUEST,
            })}
          </p>
          <code className="break-all">{profileHeader}</code>
        </>
      )}
      <a
        href={API_DOCS_PATH}
        target="_blank"
        rel="noreferrer"
        className="underline underline-offset-4"
      >
        {t("web.settings.apiKeys.usage.docs")}
      </a>
    </section>
  );
}

function RevokeKeyDialog({
  apiKey,
  profileId,
  onClose,
}: {
  apiKey: PersonalAPIKey;
  profileId: string;
  onClose: () => void;
}) {
  const t = useT();
  const revoke = useRevokePersonalApiKey(profileId);
  const [failed, setFailed] = useState(false);

  async function confirm() {
    setFailed(false);
    try {
      await revoke.mutateAsync(apiKey.id);
      toast.success(t("web.settings.apiKeys.revoke.done"));
      onClose();
    } catch {
      setFailed(true);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !revoke.isPending) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("web.settings.apiKeys.revoke.title")}</DialogTitle>
          <DialogDescription>
            {t("web.settings.apiKeys.revoke.description", { label: apiKey.label })}
          </DialogDescription>
        </DialogHeader>
        {failed ? <p role="alert">{t("web.settings.apiKeys.revoke.failed")}</p> : null}
        <DialogFooter>
          <Button variant="outline" disabled={revoke.isPending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="destructive" disabled={revoke.isPending} onClick={() => void confirm()}>
            {revoke.isPending
              ? t("web.settings.apiKeys.revoke.working")
              : t("web.settings.apiKeys.list.revoke")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
