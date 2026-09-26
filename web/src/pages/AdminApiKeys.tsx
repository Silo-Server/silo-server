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
import { V2ProblemError } from "@/api/v2/request";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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

type Tier = AdminAPIKeyMetadata["rate_tier"];

// `rate_tier` only selects which request rate limit the key's own counter
// uses (Security & access → rate limits). It never changes what the key can
// reach, so the page calls it a rate limit rather than a tier.
const RATE_LIMIT_LABELS: Record<Tier, string> = {
  standard: "Standard",
  elevated: "Elevated",
};

function rateLimitLabel(tier: Tier): string {
  return RATE_LIMIT_LABELS[tier] ?? tier;
}

function formatRateLimit(limit: { requests_per_second: number; requests_per_minute: number }) {
  const format = new Intl.NumberFormat();
  return `${format.format(limit.requests_per_second)} requests/s, ${format.format(limit.requests_per_minute)}/min`;
}
function message(error: unknown) {
  return error instanceof Error ? error.message : "The request could not be completed.";
}

export default function AdminApiKeys() {
  useAuth();
  return <ApiKeyManager key={adminApiKeyScope()} />;
}
function ApiKeyManager() {
  const capability = useAdminApiKeyCapabilities();
  const viewerId = useAuth().user?.id;
  // Only the server Owner may change or revoke the Owner's keys. Until the
  // account list loads successfully, nobody is offered the actions.
  const accounts = useAdminUsers();
  const ownerId = accounts.data?.find((u) => u.is_owner)?.id;
  const ownerLocked = (userId: string) =>
    accounts.isPending ||
    accounts.isError ||
    (ownerId !== undefined && ownerId !== viewerId && Number(userId) === ownerId);
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
      setError(message(e));
    } finally {
      openingRef.current = false;
      setOpening(false);
    }
  }
  return (
    <div className="page-shell flex flex-col gap-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="flex flex-col gap-3">
          <h1 className="page-title">API Keys</h1>
          <p className="page-subtitle">
            Create and manage credentials for integrations and automation.
          </p>
        </div>
        <Button
          size="sm"
          disabled={!capability.data?.available}
          onClick={() => setCreateOpen(true)}
        >
          <Plus data-icon="inline-start" />
          Create Key
        </Button>
      </div>
      {capability.isPending ? (
        <p role="status">Checking API key management…</p>
      ) : capability.isError ? (
        <div role="alert">
          {message(capability.error)}{" "}
          <Button variant="outline" onClick={() => void capability.refetch()}>
            Retry availability
          </Button>
        </div>
      ) : !capability.data?.available ? (
        <p role="alert">API key management is unavailable.</p>
      ) : (
        <>
          {error && <p role="alert">{error}</p>}
          {keys.isError && (
            <div role="alert">
              {rows.length
                ? "Some keys could not be loaded. The rows below may be out of date."
                : "API keys could not be loaded."}{" "}
              {message(keys.error)}{" "}
              <Button variant="outline" onClick={() => void keys.restart()}>
                Reload keys
              </Button>
            </div>
          )}
          {keys.isPending ? (
            <p role="status">Loading API keys…</p>
          ) : (
            <div className="surface-panel overflow-x-auto rounded-2xl">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Label</TableHead>
                    <TableHead>User</TableHead>
                    <TableHead>Key prefix</TableHead>
                    <TableHead>Rate limit</TableHead>
                    <TableHead>Created</TableHead>
                    <TableHead>Last Used</TableHead>
                    <TableHead>Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.length === 0 && !keys.isError && (
                    <TableRow>
                      <TableCell colSpan={7}>No API keys yet.</TableCell>
                    </TableRow>
                  )}
                  {rows.map((key) => (
                    <TableRow key={key.id}>
                      <TableCell>{key.label}</TableCell>
                      <TableCell>{key.username}</TableCell>
                      <TableCell>
                        <code>{key.key_prefix ? `${key.key_prefix}…` : "Unavailable"}</code>
                      </TableCell>
                      <TableCell>{rateLimitLabel(key.rate_tier)}</TableCell>
                      <TableCell>{formatDate(key.created_at)}</TableCell>
                      <TableCell>
                        {key.last_used_at ? formatDate(key.last_used_at) : "Never"}
                      </TableCell>
                      <TableCell>
                        {!ownerLocked(key.user_id) && (
                          <div className="flex gap-2">
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={opening}
                              aria-label={`Edit rate limit for ${key.label}`}
                              onClick={() => void openEditor(key.id, "tier")}
                            >
                              Edit rate limit<span className="sr-only"> for {key.label}</span>
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              disabled={opening}
                              aria-label={`Revoke API key ${key.label}`}
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
              {keys.isFetchingNextPage ? "Loading…" : "Load more keys"}
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
            <DialogTitle>Create API Key</DialogTitle>
            <DialogDescription>The full credential is shown only once.</DialogDescription>
          </DialogHeader>
          {createOpen && (
            <CreateApiKeyForm onClose={() => setCreateOpen(false)} onBusy={setCreating} />
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
    return limit ? `${rateLimitLabel(option)} — ${formatRateLimit(limit)}` : rateLimitLabel(option);
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
          ? "This key changed. Your draft is preserved. Reload the current key before deciding whether to apply it."
          : `${message(e)} Reload the current key to check whether the change was applied.`,
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
            {mode === "tier" ? "Edit API key rate limit" : "Revoke API key"}
          </DialogTitle>
          <DialogDescription>
            {editor.body.label}
            {mode === "revoke"
              ? " — revocation cannot be undone."
              : ` — current rate limit: ${rateLimitLabel(editor.body.rate_tier)}`}
          </DialogDescription>
        </DialogHeader>
        {mode === "tier" && (
          <div className="flex flex-col gap-2">
            <Label htmlFor="key-tier">New rate limit</Label>
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
              Sets how many requests this key can make. It doesn&apos;t change what the key can
              access or its scopes.
            </p>
          </div>
        )}
        {error && <p role="alert">{error}</p>}
        {reloadRequired && (
          <Button variant="outline" disabled={busy} onClick={() => void act(true)}>
            Reload current key
          </Button>
        )}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant={mode === "revoke" ? "destructive" : "default"}
            disabled={busy || reloadRequired}
            onClick={() => void act(false)}
          >
            {busy ? "Working…" : mode === "revoke" ? "Revoke" : "Save rate limit"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
function CreateApiKeyForm({
  onClose,
  onBusy,
}: {
  onClose: () => void;
  onBusy: (busy: boolean) => void;
}) {
  const { user } = useAuth();
  const users = useAdminUsers();
  const [profileContext] = useState(captureAdminApiKeyAuthority);
  const [label, setLabel] = useState("");
  const [userId, setUserId] = useState(String(user?.id ?? ""));
  const [secret, setSecret] = useState<string | null>(null);
  const [error, setError] = useState("");
  const lock = useRef(false);
  const create = useAdminCreateApiKey();
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (lock.current) return;
    lock.current = true;
    onBusy(true);
    setError("");
    try {
      const result = await create.mutateAsync({
        body: { label, ...(userId ? { user_id: userId } : {}) },
        profileContext,
      });
      if (isCapturedProfileAuthorityActive(profileContext)) setSecret(result.key);
    } catch (e) {
      setError(
        e instanceof V2ProblemError && e.status < 500
          ? message(e)
          : "The creation result is uncertain. Reload the key list before deciding whether to create another key; its secret cannot be recovered from the list.",
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
      setError("Couldn't copy — select the key and copy it manually");
      return;
    }
    toast.success("Copied to clipboard");
    onClose();
  }
  if (secret)
    return (
      <div className="flex flex-col gap-4">
        <p>Copy your API key now. You cannot retrieve the full key again.</p>
        <code className="break-all">{secret}</code>
        {error && <p role="alert">{error}</p>}
        <Button onClick={() => void copy()}>
          <Copy data-icon="inline-start" />
          Copy &amp; Close
        </Button>
      </div>
    );
  return (
    <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <Label htmlFor="key-label">Label</Label>
        <Input id="key-label" value={label} onChange={(e) => setLabel(e.target.value)} required />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="key-user">User</Label>
        <Select value={userId} onValueChange={setUserId}>
          <SelectTrigger id="key-user">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {users.data
                ?.filter((u) => canManageAccount(u, user?.id))
                .map((u) => (
                  <SelectItem key={u.id} value={String(u.id)}>
                    {u.username}
                  </SelectItem>
                ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      {users.isError && (
        <p role="alert">
          The user list could not be refreshed. The selected account remains unchanged.
        </p>
      )}
      {error && <p role="alert">{error}</p>}
      <Button type="submit" disabled={create.isPending}>
        {create.isPending ? "Creating…" : "Create"}
      </Button>
    </form>
  );
}
