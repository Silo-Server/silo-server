import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { useAuth } from "@/hooks/useAuth";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useRateLimitConfig } from "@/hooks/queries/admin/rateLimits";
import {
  useAdminApiKeys,
  useAdminApiKeyCapabilities,
  useAdminCreateApiKey,
  useAdminDeleteApiKey,
  useAdminUpdateApiKeyTier,
} from "@/hooks/queries/admin/apiKeys";
import {
  adminApiKeyScope,
  captureAdminApiKeyAuthority,
  getAdminApiKey,
  type AdminAPIKeyEditor,
  type AdminAPIKeyMetadata,
} from "@/api/v2/adminApiKeys";
import { isCapturedProfileAuthorityActive } from "@/api/client";
import type { components } from "@/api/v2/schema";
import { V2ProblemError } from "@/api/v2/request";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { RadioCardItem, RadioGroup } from "@/components/ui/radio-group";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Copy, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { canManageAccount } from "@/lib/accountOwner";
import { copyTextToClipboard } from "@/lib/clipboard";
import { formatDate } from "@/lib/datetime";
import { formatLocale } from "@/lib/appLocale";
import { useT, type MessageKey, type Translate } from "@/i18n";

type Tier = AdminAPIKeyMetadata["rate_tier"];
type Scope = components["schemas"]["APIKeyScope"];

// `rate_tier` only selects which request rate limit the key's own counter
// uses (Security & access → rate limits). It never changes what the key can
// reach, so the page calls it a rate limit rather than a tier.
const RATE_LIMIT_LABELS: Record<Tier, MessageKey> = {
  standard: "web.admin.apiKeys.rateLimit.standard",
  elevated: "web.admin.apiKeys.rateLimit.elevated",
};

function rateLimitLabel(t: Translate, tier: Tier): string {
  const key = RATE_LIMIT_LABELS[tier];
  return key ? t(key) : tier;
}

function formatRateLimit(
  t: Translate,
  limit: { requests_per_second: number; requests_per_minute: number },
) {
  const format = new Intl.NumberFormat(formatLocale());
  return t("web.admin.apiKeys.rateLimit.values", {
    per_second: format.format(limit.requests_per_second),
    per_minute: format.format(limit.requests_per_minute),
  });
}
// An empty scope list is how the server stores a full-access key.
function KeyAccess({ scopes }: { scopes: string[] }) {
  const t = useT();
  if (!scopes.length) return <span>{t("web.admin.apiKeys.access.full")}</span>;
  return (
    <div className="flex max-w-xs flex-wrap gap-1">
      {scopes.map((scope) => (
        <Badge key={scope} variant="outline" className="font-mono">
          {scope}
        </Badge>
      ))}
    </div>
  );
}
function message(t: Translate, error: unknown) {
  return error instanceof Error ? error.message : t("web.admin.apiKeys.error.requestFailed");
}

export default function AdminApiKeys() {
  useAuth();
  return <ApiKeyManager key={adminApiKeyScope()} />;
}
function ApiKeyManager() {
  const t = useT();
  const capability = useAdminApiKeyCapabilities();
  const viewerId = useAuth().user?.id;
  // Only the server Owner may change or revoke another admin's keys, and
  // nobody else may touch the Owner's. Until the account list loads
  // successfully, nobody is offered the actions.
  const accounts = useAdminUsers();
  const viewerIsOwner = accounts.data?.some((u) => u.id === viewerId && u.is_owner) ?? false;
  const ownerLocked = (userId: string) => {
    if (accounts.isPending || accounts.isError) return true;
    const account = accounts.data?.find((u) => u.id === Number(userId));
    return account !== undefined && !canManageAccount(account, viewerId, viewerIsOwner);
  };
  const keys = useAdminApiKeys(capability.data?.available === true);
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [selection, setSelection] = useState<{
    editor: AdminAPIKeyEditor;
    mode: "tier" | "revoke";
  } | null>(null);
  const [opening, setOpening] = useState(false);
  const [error, setError] = useState("");
  const openingRef = useRef(false);
  const seen = new Set<string>();
  const rows =
    keys.data?.pages
      .flatMap((page) => page.items)
      .filter((row) => {
        if (seen.has(row.id)) return false;
        seen.add(row.id);
        return true;
      }) ?? [];
  async function openEditor(id: string, mode: "tier" | "revoke") {
    if (openingRef.current) return;
    openingRef.current = true;
    setOpening(true);
    setError("");
    try {
      const editor = await getAdminApiKey(id);
      if (isCapturedProfileAuthorityActive(editor.profileContext)) setSelection({ editor, mode });
    } catch (e) {
      setError(message(t, e));
    } finally {
      openingRef.current = false;
      setOpening(false);
    }
  }
  return (
    <div className="space-y-6 pb-4 sm:pb-6">
      <PageHeader
        title={t("web.admin.apiKeys.title")}
        subtitle={t("web.admin.apiKeys.subtitle")}
        actions={
          <Button
            size="sm"
            disabled={!capability.data?.available}
            onClick={() => setCreateOpen(true)}
          >
            <Plus data-icon="inline-start" />
            {t("web.admin.apiKeys.createKey")}
          </Button>
        }
      />
      {capability.isPending ? (
        <p role="status">{t("web.admin.apiKeys.checking")}</p>
      ) : capability.isError ? (
        <div role="alert">
          {t("web.admin.apiKeys.retryAvailability", {
            error: message(t, capability.error),
            retry: (chunks) => (
              <Button variant="outline" onClick={() => void capability.refetch()}>
                {chunks}
              </Button>
            ),
          })}
        </div>
      ) : !capability.data?.available ? (
        <p role="alert">{t("web.admin.apiKeys.unavailable")}</p>
      ) : (
        <>
          {error && <p role="alert">{error}</p>}
          {keys.isError && (
            <div role="alert">
              {t(
                rows.length
                  ? "web.admin.apiKeys.list.partialError"
                  : "web.admin.apiKeys.list.loadError",
                {
                  error: message(t, keys.error),
                  reload: (chunks) => (
                    <Button variant="outline" onClick={() => void keys.restart()}>
                      {chunks}
                    </Button>
                  ),
                },
              )}
            </div>
          )}
          {keys.isPending ? (
            <p role="status">{t("web.admin.apiKeys.list.loading")}</p>
          ) : (
            <div className="surface-panel overflow-x-auto rounded-2xl">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("web.admin.apiKeys.list.column.label")}</TableHead>
                    <TableHead>{t("web.admin.apiKeys.list.column.user")}</TableHead>
                    <TableHead>{t("web.admin.apiKeys.list.column.prefix")}</TableHead>
                    <TableHead>{t("web.admin.apiKeys.list.column.access")}</TableHead>
                    <TableHead>{t("web.admin.apiKeys.list.column.rateLimit")}</TableHead>
                    <TableHead>{t("web.admin.apiKeys.list.column.created")}</TableHead>
                    <TableHead>{t("web.admin.apiKeys.list.column.lastUsed")}</TableHead>
                    <TableHead>{t("web.admin.apiKeys.list.column.actions")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.length === 0 && !keys.isError && (
                    <TableRow>
                      <TableCell colSpan={8}>{t("web.admin.apiKeys.list.empty")}</TableCell>
                    </TableRow>
                  )}
                  {rows.map((key) => (
                    <TableRow key={key.id}>
                      <TableCell>{key.label}</TableCell>
                      <TableCell>{key.username}</TableCell>
                      <TableCell>
                        <code>
                          {key.key_prefix
                            ? t("web.admin.apiKeys.list.prefix", { prefix: key.key_prefix })
                            : t("web.admin.apiKeys.list.prefixUnavailable")}
                        </code>
                      </TableCell>
                      <TableCell>
                        <KeyAccess scopes={key.scopes} />
                      </TableCell>
                      <TableCell>{rateLimitLabel(t, key.rate_tier)}</TableCell>
                      <TableCell>{formatDate(key.created_at)}</TableCell>
                      <TableCell>
                        {key.last_used_at
                          ? formatDate(key.last_used_at)
                          : t("web.admin.apiKeys.list.neverUsed")}
                      </TableCell>
                      <TableCell>
                        {!ownerLocked(key.user_id) && (
                          <div className="flex gap-2">
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={opening}
                              aria-label={t("web.admin.apiKeys.list.editRateLimitAriaLabel", {
                                label: key.label,
                              })}
                              onClick={() => void openEditor(key.id, "tier")}
                            >
                              {t("web.admin.apiKeys.list.editRateLimit", {
                                label: key.label,
                                hidden: (chunks) => <span className="sr-only">{chunks}</span>,
                              })}
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              disabled={opening}
                              aria-label={t("web.admin.apiKeys.list.revokeAriaLabel", {
                                label: key.label,
                              })}
                              onClick={() => void openEditor(key.id, "revoke")}
                            >
                              <Trash2 />
                            </Button>
                          </div>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
          {keys.hasNextPage && (
            <Button
              variant="outline"
              disabled={keys.isFetching || keys.isError}
              onClick={() => void keys.fetchNextPage()}
            >
              {keys.isFetchingNextPage
                ? t("web.admin.apiKeys.list.loadingMore")
                : t("web.admin.apiKeys.list.loadMore")}
            </Button>
          )}
        </>
      )}
      <Dialog
        open={createOpen}
        onOpenChange={(open) => {
          if (!creating) setCreateOpen(open);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("web.admin.apiKeys.create.title")}</DialogTitle>
            <DialogDescription>{t("web.admin.apiKeys.create.description")}</DialogDescription>
          </DialogHeader>
          {createOpen && (
            <CreateApiKeyForm
              scopes={capability.data?.scopes ?? []}
              onClose={() => setCreateOpen(false)}
              onBusy={setCreating}
            />
          )}
        </DialogContent>
      </Dialog>
      {selection && (
        <ApiKeyEditor
          key={selection.editor.body.id + selection.mode}
          initial={selection.editor}
          mode={selection.mode}
          onClose={() => setSelection(null)}
        />
      )}
    </div>
  );
}
function ApiKeyEditor({
  initial,
  mode,
  onClose,
}: {
  initial: AdminAPIKeyEditor;
  mode: "tier" | "revoke";
  onClose: () => void;
}) {
  const t = useT();
  const [editor, setEditor] = useState(initial);
  const [tier, setTier] = useState<Tier>(initial.body.rate_tier);
  const [error, setError] = useState("");
  const [reloadRequired, setReloadRequired] = useState(false);
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const update = useAdminUpdateApiKeyTier();
  const revoke = useAdminDeleteApiKey();
  // Shows each option's configured limits; the names alone still work if the
  // limiter config can't be read.
  const rateLimits = useRateLimitConfig();
  const optionLabel = (option: Tier) => {
    const limit = rateLimits.data?.tiers?.[option];
    return limit
      ? t("web.admin.apiKeys.rateLimit.option", {
          name: rateLimitLabel(t, option),
          limits: formatRateLimit(t, limit),
        })
      : rateLimitLabel(t, option);
  };
  async function act(reload: boolean) {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    setError("");
    try {
      if (reload) {
        setEditor(await getAdminApiKey(editor.body.id, editor.profileContext));
        setReloadRequired(false);
      } else {
        if (mode === "tier") await update.mutateAsync({ editor, tier });
        else await revoke.mutateAsync(editor);
        onClose();
      }
    } catch (e) {
      setReloadRequired(true);
      setError(
        e instanceof V2ProblemError && e.status === 412
          ? t("web.admin.apiKeys.editor.conflict")
          : t("web.admin.apiKeys.editor.failed", { error: message(t, e) }),
      );
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {mode === "tier"
              ? t("web.admin.apiKeys.editor.titleRateLimit")
              : t("web.admin.apiKeys.editor.titleRevoke")}
          </DialogTitle>
          <DialogDescription>
            {mode === "revoke"
              ? t("web.admin.apiKeys.editor.descriptionRevoke", { label: editor.body.label })
              : t("web.admin.apiKeys.editor.descriptionRateLimit", {
                  label: editor.body.label,
                  rate_limit: rateLimitLabel(t, editor.body.rate_tier),
                })}
          </DialogDescription>
        </DialogHeader>
        {mode === "tier" && (
          <div className="flex flex-col gap-2">
            <Label htmlFor="key-tier">{t("web.admin.apiKeys.editor.newRateLimit")}</Label>
            <Select value={tier} onValueChange={(value) => setTier(value as Tier)} disabled={busy}>
              <SelectTrigger id="key-tier">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="standard">{optionLabel("standard")}</SelectItem>
                  <SelectItem value="elevated">{optionLabel("elevated")}</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-sm">
              {t("web.admin.apiKeys.editor.rateLimitHelp")}
            </p>
          </div>
        )}
        {error && <p role="alert">{error}</p>}
        {reloadRequired && (
          <Button variant="outline" disabled={busy} onClick={() => void act(true)}>
            {t("web.admin.apiKeys.editor.reload")}
          </Button>
        )}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant={mode === "revoke" ? "destructive" : "default"}
            disabled={busy || reloadRequired}
            onClick={() => void act(false)}
          >
            {busy
              ? t("web.admin.apiKeys.editor.working")
              : mode === "revoke"
                ? t("web.admin.apiKeys.editor.revoke")
                : t("web.admin.apiKeys.editor.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
function CreateApiKeyForm({
  scopes,
  onClose,
  onBusy,
}: {
  scopes: Scope[];
  onClose: () => void;
  onBusy: (busy: boolean) => void;
}) {
  const t = useT();
  const { user } = useAuth();
  const users = useAdminUsers();
  const viewerIsOwner = users.data?.some((u) => u.id === user?.id && u.is_owner) ?? false;
  const [profileContext] = useState(captureAdminApiKeyAuthority);
  const [label, setLabel] = useState("");
  const [userId, setUserId] = useState(String(user?.id ?? ""));
  const [access, setAccess] = useState<"full" | "scoped">("full");
  const [chosen, setChosen] = useState<string[]>([]);
  // The server reads an empty list as full access, so a scoped key needs at
  // least one scope before it can be created.
  const scopedWithoutScopes = access === "scoped" && chosen.length === 0;
  const [secret, setSecret] = useState<string | null>(null);
  const [error, setError] = useState("");
  const lock = useRef(false);
  const create = useAdminCreateApiKey();
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (lock.current || scopedWithoutScopes) return;
    lock.current = true;
    onBusy(true);
    setError("");
    try {
      const result = await create.mutateAsync({
        body: {
          label,
          ...(userId ? { user_id: userId } : {}),
          ...(access === "scoped" ? { scopes: chosen } : {}),
        },
        profileContext,
      });
      if (isCapturedProfileAuthorityActive(profileContext)) setSecret(result.key);
    } catch (e) {
      setError(
        e instanceof V2ProblemError && e.status < 500
          ? message(t, e)
          : t("web.admin.apiKeys.create.uncertain"),
      );
    } finally {
      lock.current = false;
      onBusy(false);
      create.reset();
    }
  }
  async function copy() {
    // The key is shown once, so a failed copy must leave the dialog open.
    // copyTextToClipboard falls back to execCommand on insecure origins,
    // where navigator.clipboard is undefined (#985).
    try {
      await copyTextToClipboard(secret!);
    } catch {
      setError(t("web.admin.apiKeys.create.copyFailed"));
      return;
    }
    toast.success(t("web.admin.apiKeys.create.copied"));
    onClose();
  }
  if (secret)
    return (
      <div className="flex flex-col gap-4">
        <p>{t("web.admin.apiKeys.create.secretNotice")}</p>
        <code className="break-all">{secret}</code>
        {error && <p role="alert">{error}</p>}
        <Button onClick={() => void copy()}>
          <Copy data-icon="inline-start" />
          {t("web.admin.apiKeys.create.copyAndClose")}
        </Button>
      </div>
    );
  return (
    <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <Label htmlFor="key-label">{t("web.admin.apiKeys.create.label")}</Label>
        <Input id="key-label" value={label} onChange={(e) => setLabel(e.target.value)} required />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="key-user">{t("web.admin.apiKeys.create.user")}</Label>
        <Select value={userId} onValueChange={setUserId}>
          <SelectTrigger id="key-user">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {users.data
                ?.filter((u) => canManageAccount(u, user?.id, viewerIsOwner))
                .map((u) => (
                  <SelectItem key={u.id} value={String(u.id)}>
                    {u.username}
                  </SelectItem>
                ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      {scopes.length > 0 && (
        <fieldset className="flex min-w-0 flex-col gap-2">
          <legend className="mb-2 text-sm font-medium">
            {t("web.admin.apiKeys.create.access")}
          </legend>
          <RadioGroup
            value={access}
            onValueChange={(value) => setAccess(value as "full" | "scoped")}
            aria-label={t("web.admin.apiKeys.create.access")}
          >
            <RadioCardItem
              value="full"
              label={t("web.admin.apiKeys.access.full")}
              hint={t("web.admin.apiKeys.create.accessFullHint")}
            />
            <RadioCardItem
              value="scoped"
              label={t("web.admin.apiKeys.create.accessScoped")}
              hint={t("web.admin.apiKeys.create.accessScopedHint")}
            />
          </RadioGroup>
          {access === "scoped" && (
            <div className="flex flex-col gap-3 pt-1">
              {scopes.map((scope) => {
                const id = `key-scope-${scope.name}`;
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
                        {scope.description}
                      </p>
                    </div>
                  </div>
                );
              })}
              <p className="text-muted-foreground text-sm">
                {t("web.admin.apiKeys.create.scopesHint")}
              </p>
            </div>
          )}
        </fieldset>
      )}
      {users.isError && <p role="alert">{t("web.admin.apiKeys.create.usersError")}</p>}
      {error && <p role="alert">{error}</p>}
      <Button type="submit" disabled={create.isPending || scopedWithoutScopes}>
        {create.isPending
          ? t("web.admin.apiKeys.create.creating")
          : t("web.admin.apiKeys.create.submit")}
      </Button>
    </form>
  );
}
