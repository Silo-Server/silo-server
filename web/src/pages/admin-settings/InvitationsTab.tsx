import { useEffect, useMemo, useRef, useState } from "react";
import { useAuth } from "@/hooks/useAuth";
import type { FormEvent } from "react";
import {
  captureInvitationAuthority,
  invitationScope,
  type AdminInvitation as Invitation,
  type InvitationDelivery,
  type InvitationAuthority,
} from "@/api/v2/invitations";
type InvitationStatus = Invitation["status"];
import {
  useAdminInvitations,
  useInvitationCapabilities,
  useCreateInvitation,
  useResendInvitation,
  useRevokeInvitation,
} from "@/hooks/queries/admin/invitations";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { effectiveAccessGroupID } from "@/components/UserPolicyFields";
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

import { Copy, MailPlus, RotateCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { INVALID_EMAIL_MESSAGE, isValidEmail } from "@/lib/email";
import { formatDate } from "@/lib/datetime";

// The claim-link box shown after create/resend. min-w-0 + overflow-hidden on
// every level matters: the URL is one unbreakable token, and without them it
// forces the dialog wider than the viewport on phones.
function ClaimLinkBox({
  claimUrl,
  finePrint,
  onCopy,
  onDone,
}: {
  claimUrl: string;
  finePrint: string;
  onCopy: (text: string) => Promise<void>;
  onDone: () => void;
}) {
  return (
    <div className="min-w-0 space-y-4">
      <div className="bg-muted min-w-0 overflow-hidden rounded-md p-2.5">
        <code className="block truncate text-xs">{claimUrl}</code>
      </div>
      <p className="text-muted-foreground text-xs">{finePrint}</p>
      <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <Button variant="outline" onClick={() => onCopy(claimUrl)}>
          <Copy className="mr-1.5 h-4 w-4" /> Copy link
        </Button>
        <Button onClick={onDone}>Done</Button>
      </div>
    </div>
  );
}

const STATUS_BADGES: Record<InvitationStatus, { label: string; variant: "default" | "outline" }> = {
  pending: { label: "Pending", variant: "default" },
  accepted: { label: "Accepted", variant: "outline" },
  expired: { label: "Expired", variant: "outline" },
  revoked: { label: "Revoked", variant: "outline" },
};

function deliveryMessage(result: InvitationDelivery) {
  if (result.delivery_status === "sent")
    return `Email sent to ${result.invitation.email}. Recipient delivery is not guaranteed. You can copy the link below.`;
  if (result.delivery_status === "not_configured")
    return "Email is not configured. The invitation was created; deliver this link yourself.";
  return "The invitation was created, but email delivery failed or is uncertain. This link remains active; deliver it yourself.";
}
function useMounted() {
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return mounted;
}
export default function InvitationsTab() {
  useAuth();
  return <InvitationManager key={invitationScope()} />;
}
function InvitationManager() {
  const capabilities = useInvitationCapabilities();
  const available = capabilities.data?.state === "available";
  const history = useAdminInvitations(available);
  const invitations = history.data?.pages.flatMap((page) => page.items) ?? [];
  const resend = useResendInvitation();
  const revoke = useRevokeInvitation();
  const mounted = useMounted();
  const busy = useRef(false);
  const createBusy = useRef(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [confirmRevoke, setConfirmRevoke] = useState<{
    row: Invitation;
    profileContext: InvitationAuthority;
  } | null>(null);
  const [revokeError, setRevokeError] = useState("");
  const [resendOpen, setResendOpen] = useState(false);
  const [resendResult, setResendResult] = useState<InvitationDelivery | null>(null);
  const [resendError, setResendError] = useState("");
  const [copyError, setCopyError] = useState("");
  async function handleCopy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      if (mounted.current) {
        setCopyError("");
        toast.success("Copied to clipboard");
      }
    } catch {
      if (mounted.current)
        setCopyError("Could not copy the link. Select and copy it manually, or try again.");
    }
  }
  async function handleResend(id: string) {
    if (busy.current) return;
    busy.current = true;
    setResendOpen(true);
    setResendError("");
    setResendResult(null);
    setCopyError("");
    try {
      const result = await resend.mutateAsync({ id, profileContext: captureInvitationAuthority() });
      if (mounted.current) setResendResult(result);
    } catch (error) {
      if (mounted.current)
        setResendError(
          error instanceof Error
            ? error.message
            : "Unable to resend invitation. Reload history before continuing.",
        );
    } finally {
      resend.reset();
      busy.current = false;
    }
  }
  async function handleRevoke() {
    if (busy.current || !confirmRevoke) return;
    busy.current = true;
    setRevokeError("");
    try {
      await revoke.mutateAsync({
        id: confirmRevoke.row.id,
        profileContext: confirmRevoke.profileContext,
      });
      if (mounted.current) setConfirmRevoke(null);
    } catch (error) {
      if (mounted.current)
        setRevokeError(error instanceof Error ? error.message : "Unable to revoke invitation.");
    } finally {
      revoke.reset();
      busy.current = false;
    }
  }
  if (capabilities.isPending) return <p>Loading invitation capabilities...</p>;
  if (capabilities.isError)
    return (
      <div role="alert">
        Could not load invitation capabilities.{" "}
        <Button onClick={() => void capabilities.refetch()}>Reload capabilities</Button>
      </div>
    );
  if (!available) return <p>Invitations are not configured on this server.</p>;
  return (
    <div className="space-y-6">
      <Dialog
        open={confirmRevoke !== null}
        onOpenChange={(open) => {
          if (!open && !busy.current) setConfirmRevoke(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Revoke invitation</DialogTitle>
            <DialogDescription>
              Revoke the invitation for {confirmRevoke?.row.email}? Their link will stop working.
            </DialogDescription>
          </DialogHeader>
          {revokeError && <p role="alert">{revokeError}</p>}
          <Button
            variant="outline"
            disabled={revoke.isPending}
            onClick={() => {
              if (!busy.current) setConfirmRevoke(null);
            }}
          >
            Cancel
          </Button>
          <Button
            variant="destructive"
            disabled={revoke.isPending}
            onClick={() => void handleRevoke()}
          >
            Revoke
          </Button>
        </DialogContent>
      </Dialog>
      <Dialog
        open={resendOpen}
        onOpenChange={(open) => {
          if (!open && !busy.current) {
            setResendOpen(false);
            setResendResult(null);
            setCopyError("");
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Fresh invitation link</DialogTitle>
            <DialogDescription>
              {resendResult
                ? deliveryMessage(resendResult)
                : "A replacement invalidates the previous link."}
            </DialogDescription>
          </DialogHeader>
          {resend.isPending && <p>Creating a fresh invitation...</p>}
          {resendError && (
            <div role="alert">
              <p>{resendError}</p>
              <p>The outcome may be uncertain. Reload history before another deliberate resend.</p>
              <Button
                onClick={async () => {
                  try {
                    await history.restart();
                  } catch {
                    return;
                  }
                  if (mounted.current) {
                    setResendOpen(false);
                    setResendError("");
                  }
                }}
              >
                Reload history
              </Button>
            </div>
          )}
          {copyError && <p role="alert">{copyError}</p>}
          {resendResult && (
            <ClaimLinkBox
              claimUrl={resendResult.claim_url}
              finePrint="The link works once. Any previous link has stopped working."
              onCopy={handleCopy}
              onDone={() => {
                setResendOpen(false);
                setResendResult(null);
                setCopyError("");
              }}
            />
          )}
        </DialogContent>
      </Dialog>
      <div className="flex items-start justify-between gap-4">
        <p className="text-muted-foreground max-w-xl text-sm">
          Invite someone with a personal link. Their email address becomes their username.
        </p>
        <Dialog
          open={createOpen}
          onOpenChange={(open) => {
            if (!createBusy.current) {
              setCreateOpen(open);
              setCopyError("");
            }
          }}
        >
          <DialogTrigger asChild>
            <Button size="sm">
              <MailPlus className="mr-1 h-4 w-4" />
              Invite someone
            </Button>
          </DialogTrigger>
          <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
            <DialogHeader>
              <DialogTitle>Invite someone</DialogTitle>
              <DialogDescription>
                Choose their access. They choose a password using the invitation link.
              </DialogDescription>
            </DialogHeader>
            {copyError && <p role="alert">{copyError}</p>}
            <CreateInvitationForm
              defaultProfile={capabilities.data?.default_profile === true}
              onBusy={(value) => {
                createBusy.current = value;
              }}
              onReload={() => history.restart()}
              onClose={() => {
                if (!createBusy.current) {
                  setCreateOpen(false);
                  setCopyError("");
                }
              }}
              onCopy={handleCopy}
            />
          </DialogContent>
        </Dialog>
      </div>
      {resendError && !resendOpen && (
        <div role="alert">
          <p>{resendError}</p>
          <Button
            onClick={async () => {
              try {
                await history.restart();
                if (mounted.current) setResendError("");
              } catch {
                /* The history query displays the failure. */
              }
            }}
          >
            Reload history
          </Button>
        </div>
      )}
      {history.isError && (
        <div role="alert">
          <p>Could not load invitation history. {history.error.message}</p>
          <Button onClick={() => void history.restart().catch(() => {})}>Reload history</Button>
        </div>
      )}
      {history.isPending ? (
        <p>Loading invitations...</p>
      ) : invitations.length === 0 && !history.isError ? (
        <p>No invitations yet. Invite someone to get started.</p>
      ) : (
        invitations.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Recipient</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {invitations.map((inv) => (
                <InvitationRow
                  key={inv.id}
                  invitation={inv}
                  onResend={() => void handleResend(inv.id)}
                  onRevoke={() => {
                    if (busy.current) return;
                    setRevokeError("");
                    setConfirmRevoke({ row: inv, profileContext: captureInvitationAuthority() });
                  }}
                  resending={resend.isPending || revoke.isPending || !!resendError}
                />
              ))}
            </TableBody>
          </Table>
        )
      )}
      {history.hasNextPage && (
        <Button
          variant="outline"
          disabled={history.isFetchingNextPage || history.isError}
          onClick={() => void history.fetchNextPage()}
        >
          Load more
        </Button>
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
            <Button
              variant="ghost"
              size="sm"
              onClick={onRevoke}
              title="Revoke this link"
              disabled={resending}
            >
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
  defaultProfile,
  onBusy,
  onReload,
}: {
  defaultProfile: boolean;
  onBusy: (value: boolean) => void;
  onReload: () => Promise<void>;
  onClose: () => void;
  onCopy: (text: string) => Promise<void>;
}) {
  const create = useCreateInvitation();
  const { data: accessGroups = [] } = useAccessGroups();
  const { data: libraries = [] } = useAdminLibraries();
  const [email, setEmail] = useState("");
  const [emailInvalid, setEmailInvalid] = useState(false);
  const [role, setRole] = useState<"user" | "admin">("user");
  const [accessGroupID, setAccessGroupID] = useState<number | null>(null);
  const [libraryIDs, setLibraryIDs] = useState<number[] | null>(null);
  const [note, setNote] = useState("");
  const [createProfile, setCreateProfile] = useState(defaultProfile);
  const [showTour, setShowTour] = useState(true);
  // After creation we keep the dialog open to show the claim link — the
  // token is only readable in this response, so this is the one chance to
  // copy it. The delivery outcome describes what the sender confirmed.
  const [result, setResult] = useState<InvitationDelivery | null>(null);
  const [error, setError] = useState("");
  const [needsReload, setNeedsReload] = useState(false);
  const busy = useRef(false);
  const mounted = useMounted();
  const [profileContext] = useState(captureInvitationAuthority);
  const defaultGroup = useMemo(() => accessGroups.find((g) => g.is_default), [accessGroups]);
  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (busy.current || needsReload || (!defaultProfile && createProfile)) return;
    if (!isValidEmail(email)) {
      setEmailInvalid(true);
      return;
    }
    busy.current = true;
    onBusy(true);
    setError("");
    const group = effectiveAccessGroupID(role, accessGroupID);
    try {
      const delivered = await create.mutateAsync({
        profileContext,
        body: {
          email,
          role,
          access_group_id: group == null ? undefined : String(group),
          library_ids: libraryIDs == null ? undefined : libraryIDs.map(String),
          create_profile: createProfile,
          show_tour: showTour,
          note: note.trim() || undefined,
        },
      });
      if (mounted.current) setResult(delivered);
    } catch (err) {
      if (mounted.current) {
        setError(err instanceof Error ? err.message : "Unable to create invitation.");
        setNeedsReload(true);
      }
    } finally {
      create.reset();
      busy.current = false;
      onBusy(false);
    }
  }

  if (result) {
    return (
      <div className="min-w-0 space-y-4">
        <p className="text-sm">{deliveryMessage(result)}</p>
        <ClaimLinkBox
          claimUrl={result.claim_url}
          finePrint="The link works once and expires in 7 days. Resending later mints a fresh link and kills this one."
          onCopy={onCopy}
          onDone={onClose}
        />
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div role="alert">
          <p>{error}</p>
          <p>Reload history before another deliberate attempt; the invitation may already exist.</p>
          <Button
            type="button"
            onClick={async () => {
              try {
                await onReload();
              } catch {
                if (mounted.current)
                  setError(
                    "Could not reload invitation history. Try again before creating another invitation.",
                  );
                return;
              }
              if (mounted.current) {
                setNeedsReload(false);
                setError("");
              }
            }}
          >
            Reload history
          </Button>
        </div>
      )}
      {!defaultProfile && (
        <p role="status">
          This server cannot create a default profile atomically. Turn off the profile option to
          create a profileless invitation.
        </p>
      )}
      <div className="space-y-2">
        <Label htmlFor="invitation-email">Email address</Label>
        <Input
          id="invitation-email"
          type="email"
          value={email}
          onChange={(e) => {
            setEmail(e.target.value);
            if (emailInvalid) setEmailInvalid(false);
          }}
          placeholder="them@example.com"
          aria-invalid={emailInvalid || undefined}
          aria-describedby={emailInvalid ? "invitation-email-error" : undefined}
          autoFocus
          required
        />
        {emailInvalid ? (
          <p id="invitation-email-error" className="text-destructive text-xs">
            {INVALID_EMAIL_MESSAGE}
          </p>
        ) : null}
        <p className="text-muted-foreground text-xs">
          This becomes both the destination and their sign-in username.
        </p>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-2">
          <Label>Access group</Label>
          <Select
            value={role === "admin" || accessGroupID === null ? "default" : String(accessGroupID)}
            onValueChange={(v) => setAccessGroupID(v === "default" ? null : Number(v))}
            disabled={role === "admin"}
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
          <Select value={role} onValueChange={(value) => setRole(value as "user" | "admin")}>
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

      <LibraryAccessSelector
        libraries={libraries}
        value={libraryIDs}
        onChange={setLibraryIDs}
        allLabel="Inherit from access group"
        emptyHint="The account created at accept follows the selected group's library scope."
      />

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
            disabled={!defaultProfile && !createProfile}
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
          <Button type="button" variant="ghost" disabled={create.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            disabled={create.isPending || needsReload || (!defaultProfile && createProfile)}
          >
            {create.isPending ? "Sending..." : "Send invite"}
          </Button>
        </div>
      </div>
    </form>
  );
}
