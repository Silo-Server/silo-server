import { useState } from "react";
import type { FormEvent } from "react";
import {
  useAnnouncements,
  useCreateAnnouncement,
  useDeleteAnnouncement,
} from "@/hooks/queries/notifications";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import type { AnnouncementAudience } from "@/api/types";
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
import { ScrollArea } from "@/components/ui/scroll-area";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Megaphone, Plus, Trash2 } from "lucide-react";

// ─── Date helpers (same pattern as AdminUsers) ────────────────────────────────

function parseTime(value?: string | null): number | null {
  const ts = Date.parse(value ?? "");
  return Number.isNaN(ts) ? null : ts;
}

function formatDateTime(value?: string | null, fallback = "-"): string {
  const ts = parseTime(value);
  if (ts === null) return fallback;
  return new Date(ts).toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

// ─── Audience summary ─────────────────────────────────────────────────────────

type AudienceMode = "all" | "users" | "libraries";

function audienceSummary(
  audience: AnnouncementAudience,
  libraryNames: Map<number, string>,
): string {
  if (audience.all) return "Everyone";
  if (audience.user_ids && audience.user_ids.length > 0) {
    return `${audience.user_ids.length} user(s)`;
  }
  if (audience.library_ids && audience.library_ids.length > 0) {
    const names = audience.library_ids.map((id) => libraryNames.get(id) ?? String(id));
    return `Libraries: ${names.join(", ")}`;
  }
  return "—";
}

// ─── Main page ────────────────────────────────────────────────────────────────

export default function AdminAnnouncements() {
  const { data: announcements = [], isLoading } = useAnnouncements();
  const { data: libraries = [] } = useAdminLibraries();
  const deleteMutation = useDeleteAnnouncement();
  const [confirmDeleteId, setConfirmDeleteId] = useState<number | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  const libraryNames = new Map(libraries.map((l) => [l.id, l.name]));

  if (isLoading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-10 w-full rounded-lg" />
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <ConfirmDialog
        open={confirmDeleteId !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteId(null);
        }}
        title="Delete announcement?"
        description="Deleting also dismisses unread copies from user inboxes."
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => {
          if (confirmDeleteId !== null) deleteMutation.mutate(confirmDeleteId);
          setConfirmDeleteId(null);
        }}
      />

      <div className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Announcements</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Broadcast messages to all users, specific users, or library subscribers.
          </p>
        </div>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button size="sm">
              <Plus className="mr-1 h-4 w-4" /> New announcement
            </Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-lg">
            <DialogHeader>
              <DialogTitle>New announcement</DialogTitle>
            </DialogHeader>
            <CreateAnnouncementForm onClose={() => setCreateOpen(false)} />
          </DialogContent>
        </Dialog>
      </div>

      {announcements.length === 0 ? (
        <div className="text-muted-foreground flex flex-col items-center gap-3 py-16">
          <Megaphone className="h-10 w-10 opacity-40" />
          <p className="text-sm">No announcements yet.</p>
          <p className="text-xs opacity-60">
            Create an announcement to broadcast a message to users.
          </p>
        </div>
      ) : (
        <div className="surface-panel overflow-x-auto rounded-2xl border-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Title</TableHead>
                <TableHead>Audience</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Expires</TableHead>
                <TableHead className="w-20">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {announcements.map((a) => (
                <TableRow key={a.id}>
                  <TableCell className="font-medium">{a.title}</TableCell>
                  <TableCell>{audienceSummary(a.audience, libraryNames)}</TableCell>
                  <TableCell title={a.created_at}>{formatDateTime(a.created_at)}</TableCell>
                  <TableCell>{a.expires_at ? formatDateTime(a.expires_at) : "—"}</TableCell>
                  <TableCell>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      aria-label={`Delete announcement "${a.title}"`}
                      onClick={() => setConfirmDeleteId(a.id)}
                    >
                      <Trash2 className="h-3 w-3" aria-hidden="true" />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

// ─── Create form ──────────────────────────────────────────────────────────────

function CreateAnnouncementForm({ onClose }: { onClose: () => void }) {
  const { data: users = [] } = useAdminUsers();
  const { data: libraries = [] } = useAdminLibraries();
  const createMutation = useCreateAnnouncement();

  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [mode, setMode] = useState<AudienceMode>("all");
  const [selectedUserIds, setSelectedUserIds] = useState<Set<number>>(new Set());
  const [selectedLibraryIds, setSelectedLibraryIds] = useState<Set<number>>(new Set());
  const [expiresAt, setExpiresAt] = useState("");

  const titleId = "ann-title";
  const bodyId = "ann-body";
  const expiresId = "ann-expires";

  const isValid =
    title.trim().length > 0 &&
    (mode === "all" ||
      (mode === "users" && selectedUserIds.size > 0) ||
      (mode === "libraries" && selectedLibraryIds.size > 0));

  function buildAudience(): AnnouncementAudience {
    if (mode === "all") return { all: true };
    if (mode === "users") return { user_ids: [...selectedUserIds] };
    return { library_ids: [...selectedLibraryIds] };
  }

  function toggleUser(id: number) {
    setSelectedUserIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function toggleLibrary(id: number) {
    setSelectedLibraryIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!isValid) return;

    const payload: {
      title: string;
      body: string;
      audience: AnnouncementAudience;
      expires_at?: string;
    } = {
      title: title.trim(),
      body,
      audience: buildAudience(),
    };

    if (expiresAt) {
      payload.expires_at = new Date(expiresAt).toISOString();
    }

    createMutation.mutate(payload as Parameters<typeof createMutation.mutate>[0], {
      onSuccess: onClose,
    });
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {/* Title */}
      <div className="space-y-1.5">
        <Label htmlFor={titleId}>Title *</Label>
        <Input
          id={titleId}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Announcement title"
          required
        />
      </div>

      {/* Body */}
      <div className="space-y-1.5">
        <Label htmlFor={bodyId}>Body</Label>
        <textarea
          id={bodyId}
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder="Optional message body…"
          rows={3}
          className="border-input bg-background placeholder:text-muted-foreground focus-visible:ring-ring w-full rounded-md border px-3 py-2 text-sm shadow-sm focus-visible:ring-1 focus-visible:outline-none"
        />
      </div>

      {/* Audience */}
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Audience</legend>
        <div className="space-y-1">
          {(
            [
              { value: "all", label: "Everyone" },
              { value: "users", label: "Specific users" },
              { value: "libraries", label: "Libraries" },
            ] as { value: AudienceMode; label: string }[]
          ).map(({ value, label }) => (
            <label key={value} className="flex cursor-pointer items-center gap-2 text-sm">
              <input
                type="radio"
                name="audience-mode"
                value={value}
                checked={mode === value}
                onChange={() => setMode(value)}
                className="accent-primary"
              />
              {label}
            </label>
          ))}
        </div>

        {/* User list */}
        {mode === "users" && (
          <ScrollArea className="border-input max-h-40 rounded-md border p-2">
            <div className="space-y-1">
              {users.map((u) => (
                <label key={u.id} className="flex cursor-pointer items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={selectedUserIds.has(u.id)}
                    onChange={() => toggleUser(u.id)}
                    className="accent-primary"
                  />
                  <span>
                    {u.username}
                    {u.email ? ` (${u.email})` : ""}
                  </span>
                </label>
              ))}
              {users.length === 0 && (
                <p className="text-muted-foreground text-xs">No users found.</p>
              )}
            </div>
          </ScrollArea>
        )}

        {/* Library list */}
        {mode === "libraries" && (
          <ScrollArea className="border-input max-h-40 rounded-md border p-2">
            <div className="space-y-1">
              {libraries.map((lib) => (
                <label key={lib.id} className="flex cursor-pointer items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={selectedLibraryIds.has(lib.id)}
                    onChange={() => toggleLibrary(lib.id)}
                    className="accent-primary"
                  />
                  {lib.name}
                </label>
              ))}
              {libraries.length === 0 && (
                <p className="text-muted-foreground text-xs">No libraries found.</p>
              )}
            </div>
          </ScrollArea>
        )}
      </fieldset>

      {/* Expires */}
      <div className="space-y-1.5">
        <Label htmlFor={expiresId}>Expires (optional)</Label>
        <Input
          id={expiresId}
          type="datetime-local"
          value={expiresAt}
          onChange={(e) => setExpiresAt(e.target.value)}
        />
      </div>

      {/* Submit */}
      <div className="border-border border-t pt-4">
        <Button type="submit" className="w-full" disabled={!isValid || createMutation.isPending}>
          {createMutation.isPending ? "Publishing…" : "Publish"}
        </Button>
      </div>
    </form>
  );
}
