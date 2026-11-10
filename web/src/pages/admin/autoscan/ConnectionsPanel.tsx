import { useLayoutEffect, useRef, useState } from "react";
import { CheckCircle2, Pencil, Plus, Trash2, XCircle } from "lucide-react";
import { Link } from "react-router";
import type {
  AutoscanConnection,
  AutoscanConnectionInput,
  AutoscanConnectionTestInput,
  AutoscanConnectionTestResult,
} from "@/api/types";
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
  useTestAutoscanConnection,
  useUpdateAutoscanConnection,
} from "@/hooks/queries/useAutoscan";
import { useRequestIntegrations } from "@/hooks/queries/admin/requests";
import { useT } from "@/i18n";
import { FieldGroup } from "@/pages/admin-settings/FieldGroup";

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

const NO_VALUE = "—";
const BASE_URL_EXAMPLE = "http://localhost:8989";
/** Stands in for a saved API key, which is never sent back. */
const SAVED_KEY_MASK = "••••••••";

function connectionKindLabel(kind: string): string {
  if (kind === "sonarr") return "Sonarr";
  if (kind === "radarr") return "Radarr";
  return kind;
}

// integrationKind reads the arr service kind from plugin_config, which is now
// the sole source of truth (the legacy top-level `kind` column was dropped).
function integrationKind(integration: RequestIntegration): string {
  const kind = integration.plugin_config?.["service_kind"];
  return typeof kind === "string" ? kind : "";
}

function isArrKind(integration: RequestIntegration): boolean {
  const kind = integrationKind(integration);
  return kind === "sonarr" || kind === "radarr";
}

// ---------------------------------------------------------------------------
// Main panel
// ---------------------------------------------------------------------------

export default function ConnectionsPanel() {
  const t = useT();
  const connections = useAutoscanConnections();
  const requestIntegrations = useRequestIntegrations();
  const createConnection = useCreateAutoscanConnection();
  const updateConnection = useUpdateAutoscanConnection();
  const deleteConnection = useDeleteAutoscanConnection();
  const testConnection = useTestAutoscanConnection();

  const [dialog, setDialog] = useState<DialogState>(BLANK_DIALOG);
  const activeDialog = useRef<DialogState | null>(dialog);
  useLayoutEffect(() => {
    activeDialog.current = dialog;
    return () => {
      activeDialog.current = null;
    };
  }, [dialog]);
  const [deleteTarget, setDeleteTarget] = useState<AutoscanConnection | null>(null);
  const [testOutcome, setTestOutcome] = useState<{
    scope: DialogState;
    result: AutoscanConnectionTestResult;
  } | null>(null);
  const testResult = testOutcome?.scope === dialog ? testOutcome.result : null;
  function setTestResult(result: AutoscanConnectionTestResult | null) {
    setTestOutcome(result ? { scope: dialog, result } : null);
  }

  const arrIntegrations = (requestIntegrations.data ?? []).filter(isArrKind);

  // -------------------------------------------------------------------------
  // Dialog helpers
  // -------------------------------------------------------------------------

  function openAddDialog() {
    setTestResult(null);
    // Default to reusing a server already configured under Requests when one
    // exists. Silo already holds those credentials, so making "enter your own"
    // the default was asking operators to type the same API key twice — the
    // single most common piece of duplicated setup.
    const firstIntegration = arrIntegrations[0];
    setDialog({
      ...BLANK_DIALOG,
      open: true,
      mode: firstIntegration ? "reuse" : "own",
      requestIntegrationId: firstIntegration?.id ?? "",
    });
  }

  function openEditDialog(conn: AutoscanConnection) {
    setTestResult(null);
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
    setTestResult(null);
    setDialog(BLANK_DIALOG);
  }

  // -------------------------------------------------------------------------
  // Test connection (advisory — never blocks save)
  // -------------------------------------------------------------------------

  function handleTest() {
    setTestResult(null);
    let body: AutoscanConnectionTestInput;
    if (dialog.editing && dialog.mode === "own") {
      // Editing an existing own-credentials connection: test the saved record
      // unless the operator typed new credentials in this dialog session.
      if (dialog.baseUrl.trim() && dialog.apiKey.trim()) {
        body = { base_url: dialog.baseUrl.trim(), api_key_ref: dialog.apiKey.trim() };
      } else {
        body = { connection_id: dialog.editing.id };
      }
    } else if (dialog.editing && dialog.mode === "reuse") {
      body = { connection_id: dialog.editing.id };
    } else if (dialog.mode === "reuse") {
      body = { request_integration_id: dialog.requestIntegrationId };
    } else {
      body = {
        base_url: dialog.baseUrl.trim(),
        ...(dialog.apiKey.trim() ? { api_key_ref: dialog.apiKey.trim() } : {}),
      };
    }
    testConnection.mutate(body, {
      onSuccess: (result) => setTestResult(result),
      onError: (err) =>
        setTestResult({
          ok: false,
          error:
            err instanceof Error
              ? err.message
              : t("web.admin.autoscan.connections.form.testRequestFailed"),
        }),
    });
  }

  const canTest =
    dialog.mode === "reuse"
      ? Boolean(dialog.requestIntegrationId) || Boolean(dialog.editing)
      : Boolean(dialog.baseUrl.trim()) || Boolean(dialog.editing);

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
        kind: (linked && integrationKind(linked)) || "sonarr",
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
      updateConnection.mutate(
        { id: dialog.editing.id, body },
        {
          onSuccess: () => {
            if (activeDialog.current === dialog) closeDialog();
          },
        },
      );
    } else {
      createConnection.mutate(body, {
        onSuccess: () => {
          if (activeDialog.current === dialog) closeDialog();
        },
      });
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
      <FieldGroup
        label={t("web.admin.autoscan.connections.list.title")}
        description={t("web.admin.autoscan.connections.list.description")}
        actions={
          <Button variant="outline" size="sm" onClick={openAddDialog}>
            <Plus />
            {t("web.admin.autoscan.connections.list.add")}
          </Button>
        }
      >
        {connections.isLoading ? (
          <p className="text-muted-foreground py-3.5 text-sm">
            {t("web.admin.autoscan.connections.list.loading")}
          </p>
        ) : list.length === 0 ? (
          <p className="text-muted-foreground py-3.5 text-sm">
            {t("web.admin.autoscan.connections.list.empty")}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("web.admin.autoscan.connections.list.columnName")}</TableHead>
                <TableHead>{t("web.admin.autoscan.connections.list.columnKind")}</TableHead>
                <TableHead>{t("web.admin.autoscan.connections.list.columnSource")}</TableHead>
                <TableHead>{t("web.admin.autoscan.connections.list.columnURL")}</TableHead>
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
                      <Badge variant="secondary">
                        {t("web.admin.autoscan.connections.list.reused")}
                      </Badge>
                    ) : (
                      <Badge variant="outline">
                        {t("web.admin.autoscan.connections.list.own")}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="text-muted-foreground max-w-[240px] truncate">
                    {conn.base_url ?? NO_VALUE}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t("web.admin.autoscan.connections.list.editLabel")}
                        onClick={() => openEditDialog(conn)}
                      >
                        <Pencil />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t("web.admin.autoscan.connections.list.deleteLabel")}
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
      </FieldGroup>

      {/* Add / Edit Dialog */}
      <Dialog open={dialog.open} onOpenChange={(open) => !open && closeDialog()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {dialog.editing
                ? t("web.admin.autoscan.connections.dialog.editTitle")
                : t("web.admin.autoscan.connections.dialog.addTitle")}
            </DialogTitle>
            <DialogDescription>
              {t("web.admin.autoscan.connections.dialog.description")}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            {/* Mode picker (only when creating) */}
            {!dialog.editing && (
              <div className="space-y-1.5">
                <Label>{t("web.admin.autoscan.connections.dialog.type")}</Label>
                <Select
                  value={dialog.mode}
                  onValueChange={(v) => setDialog((d) => ({ ...d, mode: v as DialogMode }))}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="reuse" disabled={arrIntegrations.length === 0}>
                      {t("web.admin.autoscan.connections.dialog.typeReuse")}
                    </SelectItem>
                    <SelectItem value="own">
                      {t("web.admin.autoscan.connections.dialog.typeOwn")}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {/* Reuse mode */}
            {dialog.mode === "reuse" && (
              <div className="space-y-1.5">
                <Label>{t("web.admin.autoscan.connections.dialog.requestServer")}</Label>
                {requestIntegrations.isLoading ? (
                  <p className="text-muted-foreground text-sm">
                    {t("web.admin.autoscan.connections.dialog.loadingServers")}
                  </p>
                ) : arrIntegrations.length === 0 ? (
                  <p className="text-muted-foreground text-sm">
                    {t("web.admin.autoscan.connections.dialog.noRequestServers", {
                      link: (chunks) => (
                        <Link
                          to="/admin/settings/requests"
                          className="text-foreground underline underline-offset-2"
                        >
                          {chunks}
                        </Link>
                      ),
                    })}
                  </p>
                ) : (
                  <Select
                    value={dialog.requestIntegrationId}
                    onValueChange={(v) => {
                      setTestResult(null);
                      setDialog((d) => ({ ...d, requestIntegrationId: v }));
                    }}
                  >
                    <SelectTrigger className="w-full">
                      <SelectValue
                        placeholder={t("web.admin.autoscan.connections.dialog.selectServer")}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {arrIntegrations.map((integration) => (
                        <SelectItem key={integration.id} value={integration.id}>
                          {t("web.admin.autoscan.connections.dialog.requestServerOption", {
                            name: integration.name,
                            kind: connectionKindLabel(integrationKind(integration)),
                          })}
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
                  <Label htmlFor="conn-name">{t("web.admin.autoscan.connections.form.name")}</Label>
                  <Input
                    id="conn-name"
                    placeholder={t("web.admin.autoscan.connections.form.namePlaceholder")}
                    value={dialog.name}
                    onChange={(e) => setDialog((d) => ({ ...d, name: e.target.value }))}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="conn-kind">
                    {t("web.admin.autoscan.connections.dialog.kind")}
                  </Label>
                  <Select
                    value={dialog.kind}
                    onValueChange={(v) => setDialog((d) => ({ ...d, kind: v }))}
                  >
                    <SelectTrigger id="conn-kind" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="sonarr">{connectionKindLabel("sonarr")}</SelectItem>
                      <SelectItem value="radarr">{connectionKindLabel("radarr")}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="conn-url">
                    {t("web.admin.autoscan.connections.form.baseURL")}
                  </Label>
                  <Input
                    id="conn-url"
                    placeholder={BASE_URL_EXAMPLE}
                    value={dialog.baseUrl}
                    onChange={(e) => {
                      setTestResult(null);
                      setDialog((d) => ({ ...d, baseUrl: e.target.value }));
                    }}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="conn-key">
                    {t("web.admin.autoscan.connections.form.apiKey")}
                    {dialog.editing?.has_api_key && (
                      <span className="text-muted-foreground ml-1 font-normal">
                        {t("web.admin.autoscan.connections.dialog.apiKeyKeep")}
                      </span>
                    )}
                  </Label>
                  <Input
                    id="conn-key"
                    type="password"
                    placeholder={
                      dialog.editing?.has_api_key
                        ? SAVED_KEY_MASK
                        : t("web.admin.autoscan.connections.form.apiKeyPlaceholder")
                    }
                    value={dialog.apiKey}
                    onChange={(e) => {
                      setTestResult(null);
                      setDialog((d) => ({ ...d, apiKey: e.target.value }));
                    }}
                    autoComplete="new-password"
                  />
                </div>
              </div>
            )}
          </div>

          {/* Inline test result (advisory) */}
          {testResult && (
            <div
              className={`flex items-start gap-2 rounded-md border p-2.5 text-sm ${
                testResult.ok
                  ? "border-green-500/30 bg-green-500/10"
                  : "border-destructive/30 bg-destructive/10"
              }`}
              role="status"
            >
              {testResult.ok ? (
                <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-green-500" />
              ) : (
                <XCircle className="text-destructive mt-0.5 size-4 shrink-0" />
              )}
              <span
                className={
                  testResult.ok ? "text-green-600 dark:text-green-400" : "text-destructive"
                }
              >
                {testResult.ok
                  ? testResult.version
                    ? t("web.admin.autoscan.connections.form.connectedVersion", {
                        version: testResult.version,
                      })
                    : t("web.admin.autoscan.connections.form.connected")
                  : (testResult.error ?? t("web.admin.autoscan.connections.form.connectionFailed"))}
              </span>
            </div>
          )}

          <DialogFooter className="sm:justify-between">
            <Button
              type="button"
              variant="ghost"
              onClick={handleTest}
              disabled={!canTest || testConnection.isPending}
            >
              {testConnection.isPending
                ? t("web.admin.autoscan.connections.form.testing")
                : t("web.admin.autoscan.connections.form.test")}
            </Button>
            <div className="flex gap-2">
              <Button variant="outline" onClick={closeDialog} disabled={isSaving}>
                {t("common.cancel")}
              </Button>
              <Button onClick={handleSave} disabled={!canSave || isSaving}>
                {isSaving
                  ? t("web.admin.autoscan.connections.dialog.saving")
                  : dialog.editing
                    ? t("web.admin.autoscan.connections.dialog.saveChanges")
                    : t("web.admin.autoscan.connections.dialog.addSubmit")}
              </Button>
            </div>
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
            <AlertDialogTitle>{t("web.admin.autoscan.connections.delete.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("web.admin.autoscan.connections.delete.description", {
                name: deleteTarget?.name ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (deleteTarget) {
                  deleteConnection.mutate(deleteTarget.id);
                  setDeleteTarget(null);
                }
              }}
            >
              {t("web.admin.autoscan.connections.delete.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
