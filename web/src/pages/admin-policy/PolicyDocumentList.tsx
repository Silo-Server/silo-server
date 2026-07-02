import { Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { useSearchParams } from "react-router";

import { Badge } from "@/components/ui/badge";
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
  useCreatePolicyDocument,
  usePolicyDocuments,
  useSetPolicyDocumentEnabled,
} from "@/hooks/queries/admin/policy";

import { PolicyEditorPanel } from "./PolicyEditorPanel";
import { formatPolicyDate, formatPolicyDomain, messageFromError } from "./policyPageUtils";

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
  const createDocument = useCreatePolicyDocument();
  const setEnabled = useSetPolicyDocumentEnabled();
  const [domain, setDomain] = useState(domains[0] ?? "scope");
  const [name, setName] = useState("");
  const [formError, setFormError] = useState("");
  const [toggleError, setToggleError] = useState("");
  const selectedDocumentId = parseDocumentID(searchParams.get("document"));
  const createDomain = domains.includes(domain) ? domain : (domains[0] ?? "scope");

  const selectedExists = useMemo(
    () => documents.data?.some((document) => document.id === selectedDocumentId) ?? false,
    [documents.data, selectedDocumentId],
  );
  const editorDocumentId = selectedExists ? selectedDocumentId : undefined;

  function selectDocument(id: number) {
    const next = new URLSearchParams(searchParams);
    next.set("document", String(id));
    setSearchParams(next, { replace: true });
  }

  async function create() {
    setFormError("");
    const trimmedName = name.trim();
    if (!trimmedName) {
      setFormError("Name is required.");
      return;
    }

    try {
      const document = await createDocument.mutateAsync({
        domain: createDomain,
        name: trimmedName,
      });
      setName("");
      selectDocument(document.id);
    } catch (error) {
      setFormError(messageFromError(error, "Failed to create policy document."));
    }
  }

  async function toggleDocument(documentId: number, enabled: boolean) {
    setToggleError("");
    try {
      await setEnabled.mutateAsync({ documentId, enabled });
    } catch (error) {
      setToggleError(messageFromError(error, "Failed to update policy document."));
    }
  }

  return (
    <div className="space-y-5">
      <div className="surface-panel-subtle rounded-2xl p-4">
        <div className="grid gap-3 md:grid-cols-[180px_minmax(220px,1fr)_auto] md:items-end">
          <div className="space-y-2">
            <Label htmlFor="policy-create-domain">Domain</Label>
            <Select value={createDomain} onValueChange={setDomain}>
              <SelectTrigger id="policy-create-domain" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(domains.length ? domains : [createDomain]).map((item) => (
                  <SelectItem key={item} value={item}>
                    {formatPolicyDomain(item)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="policy-create-name">Name</Label>
            <Input
              id="policy-create-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="Evening kids profile limits"
            />
          </div>
          <Button type="button" onClick={create} disabled={createDocument.isPending}>
            <Plus className="size-4" />
            Create
          </Button>
        </div>
        {formError && <p className="text-destructive mt-3 text-sm">{formError}</p>}
      </div>

      {toggleError && (
        <div className="border-destructive/40 bg-destructive/10 text-destructive rounded-lg border px-3 py-2 text-sm">
          {toggleError}
        </div>
      )}

      <div className="grid gap-5 xl:grid-cols-[minmax(360px,0.7fr)_minmax(0,1fr)]">
        <div className="surface-panel overflow-hidden rounded-2xl border-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Domain</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Enabled</TableHead>
                <TableHead>Active</TableHead>
                <TableHead>Updated</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {documents.isLoading && (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground py-6 text-center">
                    Loading policy documents...
                  </TableCell>
                </TableRow>
              )}
              {!documents.isLoading && documents.data?.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground py-6 text-center">
                    No custom policy documents have been created.
                  </TableCell>
                </TableRow>
              )}
              {documents.data?.map((document) => (
                <TableRow
                  key={document.id}
                  className="cursor-pointer"
                  data-state={document.id === editorDocumentId ? "selected" : undefined}
                  onClick={() => selectDocument(document.id)}
                >
                  <TableCell>
                    <Badge variant="outline">{formatPolicyDomain(document.domain)}</Badge>
                  </TableCell>
                  <TableCell className="font-medium">{document.name}</TableCell>
                  <TableCell onClick={(event) => event.stopPropagation()}>
                    <Switch
                      checked={document.enabled}
                      onCheckedChange={(checked) => {
                        void toggleDocument(document.id, checked);
                      }}
                      disabled={setEnabled.isPending}
                      aria-label={`Set ${document.name} enabled`}
                    />
                  </TableCell>
                  <TableCell>
                    {document.active_version_id ? (
                      <Badge variant="secondary">ID {document.active_version_id}</Badge>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TableCell>
                  <TableCell>{formatPolicyDate(document.updated_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>

        <PolicyEditorPanel documentId={editorDocumentId} domains={domains} />
      </div>
    </div>
  );
}
