import { useState } from "react";
import { CheckCircle2, Plus, XCircle } from "lucide-react";

import type { AutoscanConnectionTestInput, AutoscanConnectionTestResult } from "@/api/types";
import { Button } from "@/components/ui/button";
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
  useCreateAutoscanConnection,
  useTestAutoscanConnection,
} from "@/hooks/queries/useAutoscan";
import { useRequestIntegrations } from "@/hooks/queries/useRequests";
import type { RequestIntegration } from "@/api/types";

export interface ConnectionOption {
  id: string;
  name: string;
  kind: string;
}

/** Sentinel values for the picker's non-connection choices. */
const NONE = "__none__";
const ADD_NEW = "__add__";

/** The arr service kind lives in plugin_config; it is the sole source of truth. */
function integrationKind(integration: RequestIntegration): string {
  const kind = integration.plugin_config?.["service_kind"];
  return typeof kind === "string" ? kind : "";
}

/**
 * Connection selector for the Add-source flow, with inline creation.
 *
 * Previously an operator who had not yet made a connection hit a dead end here:
 * the dropdown offered only "no connection", so they had to cancel, create one
 * under Advanced, and start the flow again. A connection is nullable and only
 * read at poll time, so creating one mid-flow is safe and needs no new API.
 *
 * When Requests already holds a matching Sonarr/Radarr, that is offered first —
 * Silo has the credentials, so asking for them again is pure duplication.
 */
export function InlineConnectionPicker({
  value,
  onChange,
  options,
  required,
  connectionKinds,
  idPrefix = "conn",
}: {
  /** Selected connection id, or "" for none. */
  value: string;
  onChange: (connectionId: string) => void;
  options: ConnectionOption[];
  required: boolean;
  /** Connection kinds this source accepts; empty means any. */
  connectionKinds: string[];
  idPrefix?: string;
}) {
  const createConnection = useCreateAutoscanConnection();
  const testConnection = useTestAutoscanConnection();
  const requestIntegrations = useRequestIntegrations();

  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [reuseId, setReuseId] = useState("");
  const [testResult, setTestResult] = useState<AutoscanConnectionTestResult | null>(null);

  // Requests integrations this source could bind, minus any already saved as a
  // connection — re-offering those would create a duplicate of the same server.
  const linkedIntegrationIds = new Set(options.map((option) => option.id));
  const reusable = (requestIntegrations.data ?? []).filter((integration) => {
    const kind = integrationKind(integration);
    if (!kind) return false;
    if (connectionKinds.length > 0 && !connectionKinds.includes(kind)) return false;
    return !linkedIntegrationIds.has(integration.id);
  });

  const defaultKind = connectionKinds[0] ?? "sonarr";

  function resetDraft() {
    setName("");
    setBaseUrl("");
    setApiKey("");
    setReuseId("");
    setTestResult(null);
  }

  function handleSelect(next: string) {
    if (next === ADD_NEW) {
      setAdding(true);
      // Pre-select a reusable Requests server so the common path is one click.
      setReuseId(reusable[0]?.id ?? "");
      return;
    }
    setAdding(false);
    resetDraft();
    onChange(next === NONE ? "" : next);
  }

  function handleTest() {
    setTestResult(null);
    const body: AutoscanConnectionTestInput = reuseId
      ? { request_integration_id: reuseId }
      : { base_url: baseUrl.trim(), ...(apiKey.trim() ? { api_key_ref: apiKey.trim() } : {}) };

    testConnection.mutate(body, {
      onSuccess: setTestResult,
      onError: (err) =>
        setTestResult({
          ok: false,
          error: err instanceof Error ? err.message : "Connection test failed",
        }),
    });
  }

  function handleCreate() {
    const linked = reusable.find((integration) => integration.id === reuseId);
    const body = linked
      ? {
          name: linked.name,
          kind: integrationKind(linked) || defaultKind,
          request_integration_id: linked.id,
        }
      : {
          name: name.trim(),
          kind: defaultKind,
          base_url: baseUrl.trim(),
          ...(apiKey.trim() ? { api_key_ref: apiKey.trim() } : {}),
        };

    createConnection.mutate(body, {
      onSuccess: (created) => {
        // Select what was just made, so the operator never has to find it.
        onChange(created.id);
        setAdding(false);
        resetDraft();
      },
    });
  }

  const canTest = reuseId ? true : baseUrl.trim().length > 0;
  const canCreate = reuseId
    ? true
    : name.trim().length > 0 && baseUrl.trim().length > 0 && apiKey.trim().length > 0;

  return (
    <div className="space-y-2">
      <Label htmlFor={`${idPrefix}-select`}>Which server?{required && " (required)"}</Label>

      <Select value={adding ? ADD_NEW : value || NONE} onValueChange={handleSelect}>
        <SelectTrigger id={`${idPrefix}-select`} className="w-full">
          <SelectValue placeholder="No connection" />
        </SelectTrigger>
        <SelectContent>
          {!required && <SelectItem value={NONE}>— No server needed —</SelectItem>}
          {options.map((option) => (
            <SelectItem key={option.id} value={option.id}>
              {option.name}
            </SelectItem>
          ))}
          <SelectItem value={ADD_NEW}>+ Add a server…</SelectItem>
        </SelectContent>
      </Select>

      {!adding && (
        <p className="text-muted-foreground text-xs">
          {required
            ? "This source needs credentials to reach its server."
            : "Optional — bind one if this source needs to reach a server."}
        </p>
      )}

      {adding && (
        <div className="border-border space-y-3 rounded-md border p-3">
          {reusable.length > 0 && (
            <div className="space-y-1.5">
              <Label htmlFor={`${idPrefix}-reuse`}>Server</Label>
              <Select
                value={reuseId || "__manual__"}
                onValueChange={(next) => {
                  setTestResult(null);
                  setReuseId(next === "__manual__" ? "" : next);
                }}
              >
                <SelectTrigger id={`${idPrefix}-reuse`} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {reusable.map((integration) => (
                    <SelectItem key={integration.id} value={integration.id}>
                      {integration.name} — already set up in Requests
                    </SelectItem>
                  ))}
                  <SelectItem value="__manual__">Use different credentials…</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-xs">
                Silo already has these credentials — no need to enter them again.
              </p>
            </div>
          )}

          {!reuseId && (
            <>
              <div className="space-y-1.5">
                <Label htmlFor={`${idPrefix}-name`}>Name</Label>
                <Input
                  id={`${idPrefix}-name`}
                  placeholder="My Sonarr"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor={`${idPrefix}-url`}>Base URL</Label>
                <Input
                  id={`${idPrefix}-url`}
                  placeholder="http://localhost:8989"
                  value={baseUrl}
                  onChange={(e) => {
                    setTestResult(null);
                    setBaseUrl(e.target.value);
                  }}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor={`${idPrefix}-key`}>API key</Label>
                <Input
                  id={`${idPrefix}-key`}
                  type="password"
                  placeholder="Enter API key"
                  autoComplete="new-password"
                  value={apiKey}
                  onChange={(e) => {
                    setTestResult(null);
                    setApiKey(e.target.value);
                  }}
                />
              </div>
            </>
          )}

          {testResult && (
            <div
              role="status"
              className={`flex items-start gap-2 rounded-md border p-2.5 text-sm ${
                testResult.ok
                  ? "border-success/30 bg-success/10 text-success"
                  : "border-destructive/30 bg-destructive/10 text-destructive"
              }`}
            >
              {testResult.ok ? (
                <CheckCircle2 className="mt-0.5 size-4 shrink-0" />
              ) : (
                <XCircle className="mt-0.5 size-4 shrink-0" />
              )}
              <span>
                {testResult.ok
                  ? `Connected${testResult.version ? ` (v${testResult.version})` : ""}`
                  : (testResult.error ?? "Connection failed")}
              </span>
            </div>
          )}

          <div className="flex flex-wrap justify-between gap-2">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={handleTest}
              disabled={!canTest || testConnection.isPending}
            >
              {testConnection.isPending ? "Testing…" : "Test connection"}
            </Button>
            <div className="flex gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  setAdding(false);
                  resetDraft();
                }}
                disabled={createConnection.isPending}
              >
                Cancel
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={handleCreate}
                disabled={!canCreate || createConnection.isPending}
              >
                <Plus />
                {createConnection.isPending ? "Adding…" : "Add server"}
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export default InlineConnectionPicker;
