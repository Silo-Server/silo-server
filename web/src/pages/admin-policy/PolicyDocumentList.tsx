import { ArrowLeft, Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { useSearchParams } from "react-router";

import type { PolicyDocument } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  useCreatePolicyDocument,
  usePolicyDocuments,
  useSetPolicyDocumentEnabled,
} from "@/hooks/queries/admin/policy";

import { PolicyEditorPanel } from "./PolicyEditorPanel";
import { formatPolicyDate, messageFromError } from "./policyPageUtils";
import { PolicyStatusPill, policyDocumentStatus, policyDomainMeta } from "./policyPresentation";

interface PolicyDocumentListProps {
  domains: readonly string[];
}

function parseDocumentID(value: string | null) {
  if (!value) return undefined;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

export function PolicyDocumentList({ domains }: PolicyDocumentListProps) {
  const [searchParams, setSearchParams] = useSearchParams();
  const documents = usePolicyDocuments();
  const selectedDocumentId = parseDocumentID(searchParams.get("document"));

  const selectedExists = useMemo(
    () => documents.data?.some((document) => document.id === selectedDocumentId) ?? false,
    [documents.data, selectedDocumentId],
  );

  function selectDocument(id: number | undefined) {
    const next = new URLSearchParams(searchParams);
    if (id === undefined) {
      next.delete("document");
    } else {
      next.set("document", String(id));
    }
    setSearchParams(next, { replace: true });
  }

  if (selectedExists && selectedDocumentId) {
    return (
      <div className="space-y-5">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="text-muted-foreground -ml-2"
          onClick={() => selectDocument(undefined)}
        >
          <ArrowLeft className="size-4" />
          All overrides
        </Button>
        <PolicyEditorPanel documentId={selectedDocumentId} domains={domains} />
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {documents.isLoading && (
        <p className="text-muted-foreground text-sm">Loading policy documents...</p>
      )}
      {!documents.isLoading &&
        domains.map((domain) => (
          <PolicyDomainCard
            key={domain}
            domain={domain}
            documents={(documents.data ?? []).filter((document) => document.domain === domain)}
            onSelect={selectDocument}
          />
        ))}
    </div>
  );
}

interface PolicyDomainCardProps {
  domain: string;
  documents: PolicyDocument[];
  onSelect: (id: number) => void;
}

function PolicyDomainCard({ domain, documents, onSelect }: PolicyDomainCardProps) {
  const meta = policyDomainMeta(domain);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const createDocument = useCreatePolicyDocument();
  const setEnabled = useSetPolicyDocumentEnabled();

  async function create() {
    setError("");
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Name is required.");
      return;
    }
    try {
      const document = await createDocument.mutateAsync({ domain, name: trimmedName });
      setName("");
      setCreating(false);
      onSelect(document.id);
    } catch (err) {
      setError(messageFromError(err, "Failed to create policy document."));
    }
  }

  async function toggleDocument(documentId: number, enabled: boolean) {
    setError("");
    try {
      await setEnabled.mutateAsync({ documentId, enabled });
    } catch (err) {
      setError(messageFromError(err, "Failed to update policy document."));
    }
  }

  return (
    <section className="surface-panel rounded-2xl border-0 p-5">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 items-start gap-3">
          <div className="bg-secondary text-secondary-foreground rounded-xl p-2.5">
            <meta.icon aria-hidden className="size-5" />
          </div>
          <div className="min-w-0 space-y-1">
            <h2 className="text-base font-semibold tracking-tight">{meta.title}</h2>
            <p className="text-muted-foreground max-w-prose text-sm">{meta.governs}</p>
          </div>
        </div>
        {!creating && (
          <Button type="button" variant="outline" size="sm" onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            New override
          </Button>
        )}
      </div>

      {creating && (
        <div className="border-border mt-4 flex flex-wrap items-center gap-2 border-t pt-4">
          <Input
            value={name}
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") void create();
            }}
            placeholder={
              meta.example ? `e.g. ${meta.example.replace(/[“”]/g, "")}` : "Override name"
            }
            aria-label={`New ${meta.title} override name`}
            className="max-w-sm"
            autoFocus
          />
          <Button type="button" size="sm" onClick={create} disabled={createDocument.isPending}>
            Create
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={() => {
              setCreating(false);
              setName("");
              setError("");
            }}
          >
            Cancel
          </Button>
        </div>
      )}

      {error && <p className="text-destructive mt-3 text-sm">{error}</p>}

      {documents.length > 0 ? (
        <ul className="border-border mt-4 divide-y divide-[color:var(--border)] border-t">
          {documents.map((document) => (
            <li key={document.id}>
              <div
                role="button"
                tabIndex={0}
                className="hover:bg-secondary/40 focus-visible:ring-ring/60 -mx-2 flex cursor-pointer flex-wrap items-center gap-3 rounded-lg px-2 py-3 outline-none focus-visible:ring-2"
                onClick={() => onSelect(document.id)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") onSelect(document.id);
                }}
              >
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{document.name}</span>
                <PolicyStatusPill status={policyDocumentStatus(document)} />
                <span className="text-muted-foreground hidden text-xs sm:block">
                  Updated {formatPolicyDate(document.updated_at)}
                </span>
                <span
                  onClick={(event) => event.stopPropagation()}
                  onKeyDown={(event) => event.stopPropagation()}
                >
                  <Switch
                    checked={document.enabled}
                    onCheckedChange={(checked) => {
                      void toggleDocument(document.id, checked);
                    }}
                    disabled={setEnabled.isPending}
                    aria-label={`Set ${document.name} enabled`}
                  />
                </span>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        !creating && (
          <p className="text-muted-foreground border-border mt-4 border-t pt-4 text-sm">
            The Silo baseline applies unchanged.
            {meta.example && (
              <span className="text-muted-foreground/80">
                {" "}
                Write an override like {meta.example}
              </span>
            )}
          </p>
        )
      )}
    </section>
  );
}
