import { CheckCircle2, Save, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import type {
  PolicyCompileIssue,
  PolicyCreateVersionResult,
  PolicyDocument,
  PolicyValidateResult,
  PolicyVersion,
  PolicyVersionSummary,
} from "@/api/types";
import { RegoEditor } from "@/components/policy/RegoEditor";
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
import { Input } from "@/components/ui/input";
import {
  useActivatePolicyVersion,
  useCreatePolicyVersion,
  usePolicyDocument,
  usePolicyVersion,
  usePolicyVersions,
  useValidatePolicy,
} from "@/hooks/queries/admin/policy";

import { PolicySimulatePanel } from "./PolicySimulatePanel";
import { PolicyVersionHistory } from "./PolicyVersionHistory";
import {
  compileIssuesFromError,
  defaultPolicySource,
  formatPolicyDate,
  formatPolicyDomain,
  messageFromError,
} from "./policyPageUtils";

interface PolicyEditorPanelProps {
  documentId?: number;
  domains: readonly string[];
}

interface PolicyEditorStateProps {
  document: PolicyDocument;
  domains: readonly string[];
  initialSource: string;
  versions: readonly PolicyVersionSummary[];
}

interface ValidationState {
  source: string;
  result: PolicyValidateResult;
}

interface ActivationTarget {
  id: number;
  version_number: number;
}

function activationTargetFromVersion(version: PolicyVersionSummary | undefined) {
  if (!version || !version.compiled_ok) return undefined;
  return { id: version.id, version_number: version.version_number };
}

function activationTargetFromCreate(result: PolicyCreateVersionResult) {
  if (!result.compiled_ok) return undefined;
  return { id: result.id, version_number: result.version_number };
}

function issueKey(issue: PolicyCompileIssue, index: number) {
  return `${issue.row}-${issue.col}-${issue.message}-${index}`;
}

function seedKey(document: PolicyDocument, seedVersion: PolicyVersion | undefined) {
  return `${document.id}:${seedVersion?.id ?? "template"}:${
    seedVersion?.source_sha256 ?? document.domain
  }`;
}

export function PolicyEditorPanel({ documentId, domains }: PolicyEditorPanelProps) {
  const documentQuery = usePolicyDocument(documentId);
  const versionsQuery = usePolicyVersions(documentId);
  const document = documentQuery.data;
  const latestVersionId =
    document?.active_version?.source !== undefined ? undefined : versionsQuery.data?.[0]?.id;
  const latestVersionQuery = usePolicyVersion(documentId, latestVersionId);
  const seedVersion: PolicyVersion | undefined =
    document?.active_version?.source !== undefined
      ? document.active_version
      : latestVersionQuery.data;
  const waitingForSeedSource = Boolean(
    document &&
    document.active_version?.source === undefined &&
    (versionsQuery.isLoading || (latestVersionId !== undefined && latestVersionQuery.isLoading)),
  );

  if (!documentId) {
    return (
      <div className="surface-panel-subtle text-muted-foreground rounded-2xl p-6 text-sm">
        Select a policy document to edit its custom Rego source.
      </div>
    );
  }

  if (documentQuery.isLoading || waitingForSeedSource) {
    return <p className="text-muted-foreground text-sm">Loading policy document...</p>;
  }

  if (!document) {
    return (
      <div className="border-destructive/40 bg-destructive/10 text-destructive rounded-lg border px-3 py-2 text-sm">
        Policy document could not be loaded.
      </div>
    );
  }

  const initialSource = seedVersion?.source ?? defaultPolicySource(document.domain);

  return (
    <PolicyEditorState
      key={seedKey(document, seedVersion)}
      document={document}
      domains={domains}
      initialSource={initialSource}
      versions={versionsQuery.data ?? []}
    />
  );
}

function PolicyEditorState({ document, domains, initialSource, versions }: PolicyEditorStateProps) {
  const [draft, setDraft] = useState(initialSource);
  const [comment, setComment] = useState("");
  const [issues, setIssues] = useState<PolicyCompileIssue[]>([]);
  const [validation, setValidation] = useState<ValidationState | null>(null);
  const [message, setMessage] = useState("");
  const [savedVersion, setSavedVersion] = useState<ActivationTarget | undefined>(undefined);
  const [confirmActivate, setConfirmActivate] = useState(false);
  const validatePolicy = useValidatePolicy();
  const createVersion = useCreatePolicyVersion();
  const activateVersion = useActivatePolicyVersion();
  const latestInactiveVersion = versions.find(
    (version) => version.id !== document.active_version_id && version.compiled_ok,
  );
  const activateTarget = savedVersion ?? activationTargetFromVersion(latestInactiveVersion);
  const validationMatchesDraft = validation?.source === draft;
  const canSave = validationMatchesDraft && validation.result.compiled_ok;

  async function validateDraft() {
    setMessage("");
    setIssues([]);
    try {
      const result = await validatePolicy.mutateAsync({
        domain: document.domain,
        source: draft,
      });
      setValidation({ source: draft, result });
      setIssues(result.errors);
      setMessage(result.compiled_ok ? "Validation passed." : "Validation failed.");
    } catch (error) {
      const nextIssues = compileIssuesFromError(error);
      setIssues(nextIssues);
      setValidation({
        source: draft,
        result: { compiled_ok: false, errors: nextIssues },
      });
      setMessage(
        nextIssues.length > 0
          ? "Validation failed."
          : messageFromError(error, "Validation failed."),
      );
    }
  }

  async function saveVersion() {
    setMessage("");
    if (!canSave) {
      setMessage("Validate the current draft successfully before saving a new version.");
      return;
    }

    try {
      const result = await createVersion.mutateAsync({
        documentId: document.id,
        source: draft,
        comment,
      });
      const target = activationTargetFromCreate(result);
      setSavedVersion(target);
      setComment("");
      toast.success(`Saved policy version v${result.version_number}`);
    } catch (error) {
      const nextIssues = compileIssuesFromError(error);
      setIssues(nextIssues);
      setMessage(
        nextIssues.length > 0
          ? "Server rejected the policy compile."
          : messageFromError(error, "Failed to save policy version."),
      );
    }
  }

  async function activateTargetVersion() {
    if (!activateTarget) return;

    try {
      await activateVersion.mutateAsync({ documentId: document.id, version: activateTarget.id });
      setConfirmActivate(false);
      setSavedVersion(undefined);
      toast.success(`Activated policy version v${activateTarget.version_number}`);
    } catch (error) {
      toast.error(messageFromError(error, "Failed to activate policy version"));
    }
  }

  return (
    <div className="space-y-5">
      <div className="surface-panel rounded-2xl border-0 p-4">
        <div className="flex flex-col justify-between gap-4 xl:flex-row xl:items-start">
          <div className="min-w-0 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight">{document.name}</h2>
              <Badge variant="outline">{formatPolicyDomain(document.domain)}</Badge>
              {document.enabled ? (
                <Badge variant="secondary">Enabled</Badge>
              ) : (
                <Badge variant="outline">Disabled</Badge>
              )}
              {document.active_version_id && (
                <Badge variant="secondary">Active ID {document.active_version_id}</Badge>
              )}
            </div>
            <p className="text-muted-foreground text-sm">
              Last updated {formatPolicyDate(document.updated_at)}
            </p>
          </div>

          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Button
              type="button"
              variant="outline"
              onClick={validateDraft}
              disabled={validatePolicy.isPending}
            >
              <CheckCircle2 className="size-4" />
              {validatePolicy.isPending ? "Validating..." : "Validate"}
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={saveVersion}
              disabled={createVersion.isPending}
            >
              <Save className="size-4" />
              {createVersion.isPending ? "Saving..." : "Save as new version"}
            </Button>
            <Button
              type="button"
              onClick={() => setConfirmActivate(true)}
              disabled={!activateTarget || activateVersion.isPending}
            >
              <ShieldCheck className="size-4" />
              {activateTarget ? `Activate v${activateTarget.version_number}` : "Activate"}
            </Button>
          </div>
        </div>

        <div className="mt-4">
          <Input
            value={comment}
            onChange={(event) => setComment(event.target.value)}
            placeholder="Optional version comment"
            aria-label="Policy version comment"
          />
        </div>

        {message && (
          <div
            className={`mt-4 rounded-lg border px-3 py-2 text-sm ${
              validationMatchesDraft && validation?.result.compiled_ok
                ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-300"
                : "border-warning/40 bg-warning/10 text-warning"
            }`}
          >
            {message}
          </div>
        )}

        {issues.length > 0 && (
          <div className="border-destructive/40 bg-destructive/10 text-destructive mt-4 rounded-lg border px-3 py-2">
            <h3 className="text-sm font-semibold">Compile issues</h3>
            <ul className="mt-2 space-y-1 text-xs">
              {issues.map((issue, index) => (
                <li key={issueKey(issue, index)}>
                  {issue.row > 0 ? `${issue.row}:${issue.col} ` : ""}
                  {issue.message}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>

      <RegoEditor value={draft} onChange={setDraft} issues={issues} height="520px" />

      <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_minmax(360px,0.7fr)]">
        <PolicySimulatePanel domains={domains} domain={document.domain} source={draft} />
        <PolicyVersionHistory
          documentId={document.id}
          activeVersionId={document.active_version_id}
        />
      </div>

      <AlertDialog open={confirmActivate} onOpenChange={setConfirmActivate}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Activate policy version?</AlertDialogTitle>
            <AlertDialogDescription>
              This is the go-live moment for v{activateTarget?.version_number}. New decisions will
              use this version after the policy generation reloads.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={activateTargetVersion} disabled={activateVersion.isPending}>
              Activate
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
