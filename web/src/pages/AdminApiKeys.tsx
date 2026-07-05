import { useState } from "react";
import type { FormEvent } from "react";
import type { AdminAPIKey, AdminCreateAPIKeyRequest } from "@/api/types";
import { useAuth } from "@/hooks/useAuth";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import {
  useAdminApiKeys,
  useAdminCreateApiKey,
  useAdminDeleteApiKey,
  useAdminUpdateApiKeyTier,
} from "@/hooks/queries/admin/apiKeys";
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
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Plus, Trash2, Copy } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { formatDate } from "@/lib/datetime";

function maskKey(key: string): string {
  if (key.length <= 10) return key;
  return key.slice(0, 6) + "..." + key.slice(-4);
}

const PAGE_SIZE_OPTIONS = ["25", "50", "100"] as const;

export default function AdminApiKeys() {
  const { data: keys = [], isLoading } = useAdminApiKeys();
  const deleteMutation = useAdminDeleteApiKey();
  const updateTier = useAdminUpdateApiKeyTier();
  const [createOpen, setCreateOpen] = useState(false);
  const [confirmRevokeKey, setConfirmRevokeKey] = useState<AdminAPIKey | null>(null);
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const total = keys.length;
  const paginatedKeys = keys.slice(page * pageSize, (page + 1) * pageSize);

  function handleRevoke(key: AdminAPIKey) {
    setConfirmRevokeKey(key);
  }

  function handleCopy(text: string) {
    navigator.clipboard.writeText(text);
    toast.success("Copied to clipboard");
  }

  if (isLoading)
    return (
      <div className="page-shell space-y-3 py-4 sm:py-6">
        <Skeleton className="h-10 w-full rounded-lg" />
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    );

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <ConfirmDialog
        open={confirmRevokeKey !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmRevokeKey(null);
        }}
        title="Revoke API key"
        description={`Revoke API key "${confirmRevokeKey?.label}"? This action cannot be undone.`}
        confirmLabel="Revoke"
        variant="destructive"
        onConfirm={() => {
          if (confirmRevokeKey) deleteMutation.mutate(confirmRevokeKey.id);
          setConfirmRevokeKey(null);
        }}
      />
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">API Keys</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Create and manage machine credentials for integrations, automation, and internal tools.
          </p>
        </div>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button size="sm">
              <Plus className="mr-1 h-4 w-4" /> Create Key
            </Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Create API Key</DialogTitle>
            </DialogHeader>
            <CreateApiKeyForm onClose={() => setCreateOpen(false)} />
          </DialogContent>
        </Dialog>
      </div>

      <div className="surface-panel overflow-x-auto rounded-2xl border-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Label</TableHead>
              <TableHead>User</TableHead>
              <TableHead>Key</TableHead>
              <TableHead>Tier</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Last Used</TableHead>
              <TableHead className="w-24">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.length === 0 && (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground text-center">
                  No API keys yet. Create one to get started.
                </TableCell>
              </TableRow>
            )}
            {paginatedKeys.map((key) => (
              <TableRow key={key.id}>
                <TableCell className="font-medium">{key.label}</TableCell>
                <TableCell>{key.username}</TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">
                      {maskKey(key.key)}
                    </code>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-6 w-6"
                      aria-label={`Copy API key ${key.label}`}
                      onClick={() => handleCopy(key.key)}
                    >
                      <Copy className="h-3 w-3" aria-hidden="true" />
                    </Button>
                  </div>
                </TableCell>
                <TableCell>
                  <Select
                    value={key.rate_tier}
                    onValueChange={(tier) => updateTier.mutate({ id: key.id, tier })}
                  >
                    <SelectTrigger className="w-[120px]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="standard">Standard</SelectItem>
                      <SelectItem value="elevated">Elevated</SelectItem>
                    </SelectContent>
                  </Select>
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {formatDate(key.created_at)}
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {key.last_used_at ? formatDate(key.last_used_at) : "Never"}
                </TableCell>
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    aria-label={`Revoke API key ${key.label}`}
                    onClick={() => handleRevoke(key)}
                  >
                    <Trash2 className="h-3 w-3" aria-hidden="true" />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {total > pageSize && (
          <div className="flex items-center justify-between px-4 py-4">
            <div className="flex items-center gap-4">
              <span className="text-muted-foreground text-sm">
                Showing {page * pageSize + 1}-{Math.min((page + 1) * pageSize, total)} of {total}
              </span>
              <Select
                value={String(pageSize)}
                onValueChange={(v) => {
                  setPageSize(Number(v));
                  setPage(0);
                }}
              >
                <SelectTrigger className="h-8 w-[100px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PAGE_SIZE_OPTIONS.map((size) => (
                    <SelectItem key={size} value={size}>
                      {size} rows
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setPage((p) => p - 1)}
                disabled={page === 0}
              >
                Previous
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setPage((p) => p + 1)}
                disabled={(page + 1) * pageSize >= total}
              >
                Next
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function CreateApiKeyForm({ onClose }: { onClose: () => void }) {
  const { user } = useAuth();
  const { data: users = [] } = useAdminUsers();
  const [label, setLabel] = useState("");
  const [userId, setUserId] = useState<string>(String(user?.id ?? ""));
  const [createdKey, setCreatedKey] = useState<string | null>(null);
  const createMutation = useAdminCreateApiKey();

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const body: AdminCreateAPIKeyRequest = { label };
    const parsedId = parseInt(userId, 10);
    if (!isNaN(parsedId)) {
      body.user_id = parsedId;
    }
    createMutation.mutate(body, {
      onSuccess: (data) => {
        toast.success("API key created");
        setCreatedKey(data.key);
      },
    });
  }

  function handleCopyAndClose() {
    if (createdKey) {
      navigator.clipboard.writeText(createdKey);
      toast.success("Copied to clipboard");
    }
    onClose();
  }

  if (createdKey) {
    return (
      <div className="space-y-4">
        <p className="text-muted-foreground text-sm">
          Copy your API key now. You won't be able to see the full key again.
        </p>
        <code className="bg-muted block rounded p-3 font-mono text-xs break-all">{createdKey}</code>
        <Button className="w-full" onClick={handleCopyAndClose}>
          <Copy className="mr-1 h-4 w-4" /> Copy & Close
        </Button>
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label>Label</Label>
        <Input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="e.g. CI/CD Pipeline"
          required
        />
      </div>
      <div className="space-y-2">
        <Label>User</Label>
        <Select value={userId} onValueChange={setUserId}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {users.map((u) => (
              <SelectItem key={u.id} value={String(u.id)}>
                {u.username}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <Button type="submit" className="w-full" disabled={createMutation.isPending}>
        {createMutation.isPending ? "Creating..." : "Create"}
      </Button>
    </form>
  );
}
