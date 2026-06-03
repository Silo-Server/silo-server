import { useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import type { AutoscanConnection, AutoscanConnectionInput } from "@/api/types";
import type { RequestIntegration } from "@/api/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
  useAutoscanConnections,
  useCreateAutoscanConnection,
  useDeleteAutoscanConnection,
  useUpdateAutoscanConnection,
} from "@/hooks/queries/useAutoscan";
import { useRequestIntegrations } from "@/hooks/queries/useRequests";

// ---------------------------------------------------------------------------
// Dialog mode types
// ---------------------------------------------------------------------------

type DialogMode = "reuse" | "own";

interface DialogState {
  open: boolean;
  editing: AutoscanConnection | null;
  mode: DialogMode;
  // "reuse" fields
  requestIntegrationId: string;
  // "own" fields
  name: string;
  kind: string;
  baseUrl: string;
  apiKey: string;
}

const BLANK_DIALOG: DialogState = {
  open: false,
  editing: null,
  mode: "own",
  requestIntegrationId: "",
  name: "",
  kind: "sonarr",
  baseUrl: "",
  apiKey: "",
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function connectionKindLabel(kind: string): string {
  if (kind === "sonarr") return "Sonarr";
  if (kind === "radarr") return "Radarr";
  return kind;
}

function isArrKind(integration: RequestIntegration): boolean {
  return integration.kind === "sonarr" || integration.kind === "radarr";
}

// ---------------------------------------------------------------------------
// Main panel
// ---------------------------------------------------------------------------

export default function ConnectionsPanel() {
  const connections = useAutoscanConnections();
  const requestIntegrations = useRequestIntegrations();
  const createConnection = useCreateAutoscanConnection();
  const updateConnection = useUpdateAutoscanConnection();
  const deleteConnection = useDeleteAutoscanConnection();

  const [dialog, setDialog] = useState<DialogState>(BLANK_DIALOG);
  const [deleteTarget, setDeleteTarget] = useState<AutoscanConnection | null>(null);

  const arrIntegrations = (requestIntegrations.data ?? []).filter(isArrKind);

  // -------------------------------------------------------------------------
  // Dialog helpers
  // -------------------------------------------------------------------------

  function openAddDialog() {
    setDialog({ ...BLANK_DIALOG, open: true });
  }

  function openEditDialog(conn: AutoscanConnection) {
    if (conn.request_integration_id) {
      setDialog({
        open: true,
        editing: conn,
        mode: "reuse",
        requestIntegrationId: conn.request_integration_id,
        name: conn.name,
        kind: conn.kind,
        baseUrl: "",
        apiKey: "",
      });
    } else {
      setDialog({
        open: true,
        editing: conn,
        mode: "own",
        requestIntegrationId: "",
        name: conn.name,
        kind: conn.kind,
        baseUrl: conn.base_url ?? "",
        apiKey: "",
      });
    }
  }

  function closeDialog() {
    setDialog(BLANK_DIALOG);
  }

  // -------------------------------------------------------------------------
  // Save
  // -------------------------------------------------------------------------

  function handleSave() {
    let body: AutoscanConnectionInput;

    if (dialog.mode === "reuse") {
      if (!dialog.requestIntegrationId) return;
      // Resolve name + kind from the selected integration for display purposes;
      // the backend can also derive them from the linked integration.
      const linked = arrIntegrations.find((i) => i.id === dialog.requestIntegrationId);
      body = {
        name: linked?.name ?? dialog.requestIntegrationId,
        kind: linked?.kind ?? "sonarr",
        request_integration_id: dialog.requestIntegrationId,
      };
    } else {
      if (!dialog.name.trim() || !dialog.baseUrl.trim()) return;
      body = {
        name: dialog.name.trim(),
        kind: dialog.kind,
        base_url: dialog.baseUrl.trim(),
        // Only send api_key_ref when the operator has typed something;
        // on edit with a blank field the backend keeps the stored key.
        ...(dialog.apiKey.trim() ? { api_key_ref: dialog.apiKey.trim() } : {}),
      };
    }

    if (dialog.editing) {
      updateConnection.mutate({ id: dialog.editing.id, body }, { onSuccess: closeDialog });
    } else {
      createConnection.mutate(body, { onSuccess: closeDialog });
    }
  }

  const isSaving = createConnection.isPending || updateConnection.isPending;

  const canSave =
    dialog.mode === "reuse"
      ? Boolean(dialog.requestIntegrationId)
      : Boolean(dialog.name.trim()) &&
        Boolean(dialog.baseUrl.trim()) &&
        (Boolean(dialog.apiKey.trim()) || Boolean(dialog.editing?.has_api_key));

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------

  const list = connections.data ?? [];

  return (
    <>
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between gap-3">
            <CardTitle>Connections</CardTitle>
            <Button variant="outline" size="sm" onClick={openAddDialog}>
              <Plus />
              Add connection
            </Button>
          </div>
        </CardHeader>
        <CardContent className="px-0">
          {connections.isLoading ? (
            <p className="text-muted-foreground px-6 py-4 text-sm">Loading…</p>
          ) : list.length === 0 ? (
            <p className="text-muted-foreground px-6 py-6 text-sm">
              No connections yet. Add one to link scan sources to an arr instance.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Source</TableHead>
                  <TableHead>URL</TableHead>
                  <TableHead className="w-0" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((conn) => (
                  <TableRow key={conn.id}>
                    <TableCell className="font-medium">{conn.name}</TableCell>
                    <TableCell>{connectionKindLabel(conn.kind)}</TableCell>
                    <TableCell>
                      {conn.request_integration_id ? (
                        <Badge variant="secondary">Reused from Requests</Badge>
                      ) : (
                        <Badge variant="outline">Own</Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground max-w-[240px] truncate">
                      {conn.base_url ?? "—"}
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label="Edit connection"
                          onClick={() => openEditDialog(conn)}
                        >
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label="Delete connection"
                          onClick={() => setDeleteTarget(conn)}
                        >
                          <Trash2 className="text-destructive" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* Add / Edit Dialog */}
      <Dialog open={dialog.open} onOpenChange={(open) => !open && closeDialog()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{dialog.editing ? "Edit connection" : "Add connection"}</DialogTitle>
            <DialogDescription>
              Connect a scan source to an arr instance by reusing an existing Requests integration
              or by entering your own credentials.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            {/* Mode picker (only when creating) */}
            {!dialog.editing && (
              <div className="space-y-1.5">
                <Label>Connection type</Label>
                <Select
                  value={dialog.mode}
                  onValueChange={(v) => setDialog((d) => ({ ...d, mode: v as DialogMode }))}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="own">Enter own credentials</SelectItem>
                    <SelectItem value="reuse">Reuse from Requests</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {/* Reuse mode */}
            {dialog.mode === "reuse" && (
              <div className="space-y-1.5">
                <Label>Requests integration</Label>
                {requestIntegrations.isLoading ? (
                  <p className="text-muted-foreground text-sm">Loading integrations…</p>
                ) : arrIntegrations.length === 0 ? (
                  <p className="text-muted-foreground text-sm">
                    No Sonarr/Radarr integrations found. Add one in the Requests page first.
                  </p>
                ) : (
                  <Select
                    value={dialog.requestIntegrationId}
                    onValueChange={(v) => setDialog((d) => ({ ...d, requestIntegrationId: v }))}
                  >
                    <SelectTrigger className="w-full">
                      <SelectValue placeholder="Select an integration…" />
                    </SelectTrigger>
                    <SelectContent>
                      {arrIntegrations.map((integration) => (
                        <SelectItem key={integration.id} value={integration.id}>
                          {integration.name} ({connectionKindLabel(integration.kind)})
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </div>
            )}

            {/* Own credentials mode */}
            {dialog.mode === "own" && (
              <div className="space-y-4">
                <div className="space-y-1.5">
                  <Label htmlFor="conn-name">Name</Label>
                  <Input
                    id="conn-name"
                    placeholder="My Sonarr"
                    value={dialog.name}
                    onChange={(e) => setDialog((d) => ({ ...d, name: e.target.value }))}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="conn-kind">Kind</Label>
                  <Select
                    value={dialog.kind}
                    onValueChange={(v) => setDialog((d) => ({ ...d, kind: v }))}
                  >
                    <SelectTrigger id="conn-kind" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="sonarr">Sonarr</SelectItem>
                      <SelectItem value="radarr">Radarr</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="conn-url">Base URL</Label>
                  <Input
                    id="conn-url"
                    placeholder="http://localhost:8989"
                    value={dialog.baseUrl}
                    onChange={(e) => setDialog((d) => ({ ...d, baseUrl: e.target.value }))}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="conn-key">
                    API key
                    {dialog.editing?.has_api_key && (
                      <span className="text-muted-foreground ml-1 font-normal">
                        (leave blank to keep existing)
                      </span>
                    )}
                  </Label>
                  <Input
                    id="conn-key"
                    type="password"
                    placeholder={dialog.editing?.has_api_key ? "••••••••" : "Enter API key"}
                    value={dialog.apiKey}
                    onChange={(e) => setDialog((d) => ({ ...d, apiKey: e.target.value }))}
                    autoComplete="new-password"
                  />
                </div>
              </div>
            )}
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={closeDialog} disabled={isSaving}>
              Cancel
            </Button>
            <Button onClick={handleSave} disabled={!canSave || isSaving}>
              {isSaving ? "Saving…" : dialog.editing ? "Save changes" : "Add connection"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation */}
      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete connection?</AlertDialogTitle>
            <AlertDialogDescription>
              &ldquo;{deleteTarget?.name}&rdquo; will be removed. Any scan sources bound to it will
              lose their connection and will need to be reconfigured.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (deleteTarget) {
                  deleteConnection.mutate(deleteTarget.id);
                  setDeleteTarget(null);
                }
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
