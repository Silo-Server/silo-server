import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import type { Invitation, InvitationStatus, SendInvitationResponse } from "@/api/types";
import {
  useAdminInvitations,
  useCreateInvitation,
  useResendInvitation,
  useRevokeInvitation,
} from "@/hooks/queries/admin/invitations";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Copy, MailPlus, RotateCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { formatDate } from "@/lib/datetime";

const STATUS_BADGES: Record<InvitationStatus, { label: string; variant: "default" | "outline" }> = {
  pending: { label: "Sent", variant: "default" },
  accepted: { label: "Accepted", variant: "outline" },
  expired: { label: "Expired", variant: "outline" },
  revoked: { label: "Revoked", variant: "outline" },
};

export default function InvitationsTab() {
  const { data: invitations = [], isLoading } = useAdminInvitations();
  const resend = useResendInvitation();
  const revoke = useRevokeInvitation();
  const [createOpen, setCreateOpen] = useState(false);
  const [confirmRevoke, setConfirmRevoke] = useState<Invitation | null>(null);

  function handleCopy(text: string) {
    navigator.clipboard.writeText(text);
    toast.success("Copied to clipboard");
  }

  if (isLoading) return <div>Loading invitations...</div>;

  return (
    <div className="space-y-6">
      <ConfirmDialog
        open={confirmRevoke !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmRevoke(null);
        }}
        title="Revoke invitation"
        description={`Revoke the invitation for ${confirmRevoke?.email}? Their link will stop working immediately.`}
        confirmLabel="Revoke"
        variant="destructive"
        onConfirm={() => {
          if (confirmRevoke) revoke.mutate(confirmRevoke.id);
          setConfirmRevoke(null);
        }}
      />

      <div className="flex items-start justify-between gap-4">
        <p className="text-muted-foreground max-w-xl text-sm">
          Email someone a personal link. Their access is set here, so all they choose is a password
          — their email address becomes their username.
        </p>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button size="sm">
              <MailPlus className="mr-1 h-4 w-4" /> Invite someone
            </Button>
          </DialogTrigger>
          <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
            <DialogHeader>
              <DialogTitle>Invite someone</DialogTitle>
              <DialogDescription>
                They get an email with a link. Their username is their email address, so all they
                pick is a password.
              </DialogDescription>
            </DialogHeader>
            <CreateInvitationForm onClose={() => setCreateOpen(false)} onCopy={handleCopy} />
          </DialogContent>
        </Dialog>
      </div>

      {invitations.length === 0 ? (
        <p className="text-muted-foreground py-8 text-center text-sm">
          No invitations yet. Invite someone to get started.
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Recipient</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Sent</TableHead>
              <TableHead className="w-24" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {invitations.map((inv) => (
              <InvitationRow
                key={inv.id}
                invitation={inv}
                onResend={() => resend.mutate(inv.id)}
                onRevoke={() => setConfirmRevoke(inv)}
                resending={resend.isPending}
              />
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

function InvitationRow({
  invitation,
  onResend,
  onRevoke,
  resending,
}: {
  invitation: Invitation;
  onResend: () => void;
  onRevoke: () => void;
  resending: boolean;
}) {
  const badge = STATUS_BADGES[invitation.status];
  const showResend = invitation.status === "pending" || invitation.status === "expired";
  const showRevoke = invitation.status === "pending";

  return (
    <TableRow>
      <TableCell>
        <div className="font-medium">{invitation.email}</div>
        {invitation.invited_by_name && (
          <div className="text-muted-foreground text-xs">
            Invited by {invitation.invited_by_name}
          </div>
        )}
      </TableCell>
      <TableCell className="text-muted-foreground capitalize">{invitation.role}</TableCell>
      <TableCell>
        <Badge variant={badge.variant}>{badge.label}</Badge>
        {invitation.status === "pending" && (
          <span className="text-muted-foreground ml-2 text-xs">
            expires {formatDate(invitation.expires_at)}
          </span>
        )}
      </TableCell>
      <TableCell className="text-muted-foreground text-sm">
        {formatDate(invitation.created_at)}
      </TableCell>
      <TableCell>
        <div className="flex justify-end gap-1">
          {showResend && (
            <Button
              variant="ghost"
              size="sm"
              onClick={onResend}
              disabled={resending}
              title="Resend with a fresh link"
            >
              <RotateCw className="h-4 w-4" />
            </Button>
          )}
          {showRevoke && (
            <Button variant="ghost" size="sm" onClick={onRevoke} title="Revoke this link">
              <Trash2 className="h-4 w-4" />
            </Button>
          )}
        </div>
      </TableCell>
    </TableRow>
  );
}

function CreateInvitationForm({
  onClose,
  onCopy,
}: {
  onClose: () => void;
  onCopy: (text: string) => void;
}) {
  const create = useCreateInvitation();
  const { data: accessGroups = [] } = useAccessGroups();
  const { data: libraries = [] } = useAdminLibraries();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("user");
  const [accessGroupID, setAccessGroupID] = useState<number | null>(null);
  const [libraryIDs, setLibraryIDs] = useState<number[] | null>(null);
  const [note, setNote] = useState("");
  const [createProfile, setCreateProfile] = useState(true);
  const [showTour, setShowTour] = useState(true);
  // When email isn't configured the server returns the claim URL instead of
  // sending; we keep the dialog open and show it for manual delivery.
  const [manualLink, setManualLink] = useState<string | null>(null);

  const defaultGroup = useMemo(() => accessGroups.find((g) => g.is_default), [accessGroups]);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    create.mutate(
      {
        email,
        role,
        access_group_id: accessGroupID,
        library_ids: libraryIDs,
        create_profile: createProfile,
        show_tour: showTour,
        note: note.trim() || undefined,
      },
      {
        onSuccess: (data: SendInvitationResponse) => {
          if (data.email_sent) {
            toast.success(`Invitation sent to ${data.invitation.email}`);
            onClose();
          } else if (data.claim_url) {
            setManualLink(data.claim_url);
          }
        },
      },
    );
  }

  if (manualLink) {
    return (
      <div className="space-y-4">
        <p className="text-sm">
          Email isn&apos;t configured on this server, so nothing was sent. The invitation was
          created — deliver this link yourself:
        </p>
        <div className="bg-muted flex items-center gap-2 rounded-md p-2">
          <code className="min-w-0 flex-1 truncate text-xs">{manualLink}</code>
          <Button variant="ghost" size="sm" onClick={() => onCopy(manualLink)}>
            <Copy className="h-4 w-4" />
          </Button>
        </div>
        <p className="text-muted-foreground text-xs">
          The link works once and expires in 7 days. Resending later mints a fresh link and kills
          this one.
        </p>
        <div className="flex justify-end">
          <Button onClick={onClose}>Done</Button>
        </div>
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label htmlFor="invitation-email">Email address</Label>
        <Input
          id="invitation-email"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="them@example.com"
          autoFocus
          required
        />
        <p className="text-muted-foreground text-xs">
          This becomes both the destination and their sign-in username.
        </p>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-2">
          <Label>Access group</Label>
          <Select
            value={accessGroupID === null ? "default" : String(accessGroupID)}
            onValueChange={(v) => setAccessGroupID(v === "default" ? null : Number(v))}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="default">
                {defaultGroup ? `${defaultGroup.name} (default)` : "Server default"}
              </SelectItem>
              {accessGroups
                .filter((g) => !g.is_default)
                .map((g) => (
                  <SelectItem key={g.id} value={String(g.id)}>
                    {g.name}
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label>Role</Label>
          <Select value={role} onValueChange={setRole}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user">User</SelectItem>
              <SelectItem value="admin">Admin</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <LibraryAccessSelector libraries={libraries} value={libraryIDs} onChange={setLibraryIDs} />

      <div className="space-y-2">
        <Label htmlFor="invitation-note">Personal note (optional)</Label>
        <textarea
          id="invitation-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="Hey — set yourself up whenever."
          rows={2}
          className="border-border bg-background text-foreground focus:border-ring focus:ring-ring/50 w-full resize-y rounded-md border px-3 py-2.5 text-sm shadow-xs transition-[color,box-shadow] outline-none focus:ring-[3px]"
        />
        <p className="text-muted-foreground text-xs">Appears in the email. Plain text.</p>
      </div>

      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <Label htmlFor="invitation-create-profile">Create their first profile</Label>
            <p className="text-muted-foreground text-xs">
              Named from the part before the @. They can rename it later.
            </p>
          </div>
          <Switch
            id="invitation-create-profile"
            checked={createProfile}
            onCheckedChange={setCreateProfile}
          />
        </div>
        <div className="flex items-center justify-between">
          <div>
            <Label htmlFor="invitation-show-tour">Show the feature tour on first sign-in</Label>
            <p className="text-muted-foreground text-xs">
              Walks through what this server can do, skipping anything turned off.
            </p>
          </div>
          <Switch id="invitation-show-tour" checked={showTour} onCheckedChange={setShowTour} />
        </div>
      </div>

      <div className="flex items-center justify-between pt-2">
        <p className="text-muted-foreground text-xs">Link expires in 7 days · single use</p>
        <div className="flex gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={create.isPending}>
            {create.isPending ? "Sending..." : "Send invite"}
          </Button>
        </div>
      </div>
    </form>
  );
}
