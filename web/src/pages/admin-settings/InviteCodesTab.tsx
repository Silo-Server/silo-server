import { useState } from "react";
import type { FormEvent } from "react";
import type { InviteCode } from "@/api/types";
import {
  useAdminInviteCodes,
  useCreateInviteCode,
  useUpdateInviteCode,
  useTopUpInviteCode,
  useDeleteInviteCode,
} from "@/hooks/queries/admin/inviteCodes";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
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
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { AlertTriangle, ArrowRight, Copy, Plus, PlusCircle, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { formatDate } from "@/lib/datetime";
import { Link } from "react-router";

export default function InviteCodesTab() {
  const { data: codes = [], isLoading } = useAdminInviteCodes();
  const { data: serverSettings } = useAdminServerSettings();
  const signupsEnabled = serverSettings?.["signup.enabled"] === "true";
  const updateCode = useUpdateInviteCode();
  const deleteCode = useDeleteInviteCode();
  const [createOpen, setCreateOpen] = useState(false);
  const [topUpCode, setTopUpCode] = useState<InviteCode | null>(null);
  const [confirmDeleteCode, setConfirmDeleteCode] = useState<InviteCode | null>(null);

  function handleToggleCode(code: InviteCode) {
    updateCode.mutate({ id: code.id, body: { enabled: !code.enabled } });
  }

  function handleDelete(code: InviteCode) {
    setConfirmDeleteCode(code);
  }

  function handleCopy(text: string) {
    navigator.clipboard.writeText(text);
    toast.success("Copied to clipboard");
  }

  if (isLoading) return <div>Loading invite codes...</div>;

  return (
    <div className="space-y-6">
      <ConfirmDialog
        open={confirmDeleteCode !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteCode(null);
        }}
        title="Delete invite code"
        description={`Delete invite code "${confirmDeleteCode?.code}"? This action cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => {
          if (confirmDeleteCode) deleteCode.mutate(confirmDeleteCode.id);
          setConfirmDeleteCode(null);
        }}
      />

      <Dialog
        open={topUpCode !== null}
        onOpenChange={(open) => {
          if (!open) setTopUpCode(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Top Up Invite Code</DialogTitle>
            <DialogDescription>
              Add extra uses to {topUpCode?.code}. Current usage is {topUpCode?.use_count} /{" "}
              {topUpCode?.max_uses}.
            </DialogDescription>
          </DialogHeader>
          {topUpCode && <TopUpInviteCodeForm code={topUpCode} onClose={() => setTopUpCode(null)} />}
        </DialogContent>
      </Dialog>

      <div className="flex justify-end">
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button size="sm">
              <Plus className="mr-1 h-4 w-4" /> Create Code
            </Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Create Invite Code</DialogTitle>
            </DialogHeader>
            <CreateInviteCodeForm onClose={() => setCreateOpen(false)} />
          </DialogContent>
        </Dialog>
      </div>

      {serverSettings !== undefined &&
        (signupsEnabled ? (
          <p className="text-muted-foreground text-sm">
            Codes only work while public signups are on.{" "}
            <Link
              to="/admin/settings/general"
              className="text-foreground inline-flex items-center gap-1 font-medium hover:underline"
            >
              Public signups setting
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          </p>
        ) : (
          <div className="flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
            <p className="text-[13px] leading-relaxed">
              <span className="font-medium text-amber-500">Public signups are off</span> — these
              codes won't work until you enable them.{" "}
              <Link
                to="/admin/settings/general"
                className="text-foreground inline-flex items-center gap-1 font-medium hover:underline"
              >
                Public signups setting
                <ArrowRight className="h-3 w-3" aria-hidden="true" />
              </Link>
            </p>
          </div>
        ))}

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Code</TableHead>
            <TableHead>Label</TableHead>
            <TableHead>Usage</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Created</TableHead>
            <TableHead className="w-32">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {codes.length === 0 && (
            <TableRow>
              <TableCell colSpan={6} className="text-muted-foreground text-center">
                No invite codes yet. Create one to get started.
              </TableCell>
            </TableRow>
          )}
          {codes.map((code) => (
            <TableRow key={code.id}>
              <TableCell>
                <div className="flex items-center gap-1.5">
                  <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">
                    {code.code}
                  </code>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-6 w-6"
                    onClick={() => handleCopy(code.code)}
                  >
                    <Copy className="h-3 w-3" />
                  </Button>
                </div>
              </TableCell>
              <TableCell>
                {code.label || <span className="text-muted-foreground">-</span>}
              </TableCell>
              <TableCell>
                <span className={code.use_count >= code.max_uses ? "text-destructive" : ""}>
                  {code.use_count} / {code.max_uses}
                </span>
              </TableCell>
              <TableCell>
                <div className="flex items-center gap-2">
                  <Switch checked={code.enabled} onCheckedChange={() => handleToggleCode(code)} />
                  <Badge variant={code.enabled ? "outline" : "secondary"}>
                    {code.enabled ? "Active" : "Disabled"}
                  </Badge>
                </div>
              </TableCell>
              <TableCell className="text-muted-foreground text-xs">
                {formatDate(code.created_at)}
              </TableCell>
              <TableCell>
                <div className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => setTopUpCode(code)}
                    aria-label={`Top up invite code ${code.code}`}
                  >
                    <PlusCircle className="h-3 w-3" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => handleDelete(code)}
                    aria-label={`Delete invite code ${code.code}`}
                  >
                    <Trash2 className="h-3 w-3" />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function CreateInviteCodeForm({ onClose }: { onClose: () => void }) {
  const [code, setCode] = useState("");
  const [label, setLabel] = useState("");
  const [maxUses, setMaxUses] = useState("10");
  const createMutation = useCreateInviteCode();

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const max = parseInt(maxUses, 10);
    if (isNaN(max) || max <= 0) {
      toast.error("Max uses must be a positive number");
      return;
    }
    createMutation.mutate(
      { code: code || undefined, label, max_uses: max },
      { onSuccess: onClose },
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label>Code (optional, auto-generated if empty)</Label>
        <Input
          value={code}
          onChange={(e) => setCode(e.target.value.toUpperCase())}
          placeholder="e.g. BETA2026"
        />
      </div>
      <div className="space-y-2">
        <Label>Label</Label>
        <Input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="e.g. Beta testers"
        />
      </div>
      <div className="space-y-2">
        <Label>Max uses</Label>
        <Input
          type="number"
          min="1"
          value={maxUses}
          onChange={(e) => setMaxUses(e.target.value)}
          required
        />
      </div>
      <Button type="submit" className="w-full" disabled={createMutation.isPending}>
        {createMutation.isPending ? "Creating..." : "Create"}
      </Button>
    </form>
  );
}

function TopUpInviteCodeForm({ code, onClose }: { code: InviteCode; onClose: () => void }) {
  const [additionalUses, setAdditionalUses] = useState("1");
  const topUpMutation = useTopUpInviteCode();

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const amount = parseInt(additionalUses, 10);
    if (isNaN(amount) || amount <= 0) {
      toast.error("Additional uses must be a positive number");
      return;
    }

    topUpMutation.mutate(
      { id: code.id, body: { additional_uses: amount } },
      { onSuccess: onClose },
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label>Additional uses</Label>
        <Input
          type="number"
          min="1"
          value={additionalUses}
          onChange={(e) => setAdditionalUses(e.target.value)}
          required
        />
      </div>
      <Button type="submit" className="w-full" disabled={topUpMutation.isPending}>
        {topUpMutation.isPending ? "Adding..." : "Add Uses"}
      </Button>
    </form>
  );
}
