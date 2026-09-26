import {
  policyApplyMessage,
  policyMutationMessage,
  type PolicySnapshot,
  type PolicyApplyResult,
} from "@/api/adminPolicy";
import { PolicyRevisionReview } from "./PolicyRevisionReview";
import { Check, CheckCircle2, Play, Save, ShieldCheck } from "lucide-react";
import { memo, useEffect, useState } from "react";

import type {
  PolicyCompileIssue,
  PolicyValidateResult,
  PolicyVersion,
  PolicyVersionSummary,
} from "@/api/adminPolicy";
import { RegoEditor } from "@/components/policy/RegoEditor";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
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
import { cn } from "@/lib/utils";

import { PolicySimulatePanel } from "./PolicySimulatePanel";
import { PolicyVersionHistory } from "./PolicyVersionHistory";
import { compileIssuesFromError, defaultPolicySource, messageFromError } from "./policyPageUtils";
import { PolicyStatusPill, policyDocumentStatus, policyDomainMeta } from "./policyPresentation";

interface PolicyEditorPanelProps {
  documentId?: string;
  domains: readonly string[];
}

interface PolicyEditorStateProps {
  document: PolicySnapshot;
  domains: readonly string[];
  initialSource: string;
  seedIsActive: boolean;
  seedVersion?: PolicyVersion;
  versions: readonly PolicyVersionSummary[];
  onStateChange: (state: EditorDirtyState) => void;
  newerSeed?: NewerSeedInfo;
}

interface EditorDirtyState {
  draft: string;
  pending: boolean;
}

interface NewerSeedInfo {
  versionNumber?: number;
  onAdopt: () => void;
}

interface PinnedSeed {
  document: PolicySnapshot;
  documentId: string;
  key: string;
  initialSource: string;
  seedIsActive: boolean;
  seedVersion?: PolicyVersion;
}

interface ValidationState {
  source: string;
  result: PolicyValidateResult;
}

interface ActivationTarget {
  id: string;
  version_number: number;
}

type LifecycleStep = "validate" | "save" | "activate" | "live";

const LIFECYCLE_LABELS = ["Draft", "Validated", "Saved", "Active"] as const;

function lifecycleProgress(step: LifecycleStep) {
  // Index of the first label that is NOT yet reached.
  switch (step) {
    case "validate":
      return 1;
    case "save":
      return 2;
    case "activate":
      return 3;
    case "live":
      return 4;
  }
}

function LifecycleRail({ step, liveVersion }: { step: LifecycleStep; liveVersion?: number }) {
  const reached = lifecycleProgress(step);
  return (
    <ol aria-label="Policy lifecycle" className="flex flex-wrap items-center gap-1.5">
      {LIFECYCLE_LABELS.map((label, index) => {
        const done = index < reached;
        const current = index === reached;
        return (
          <li key={label} className="flex items-center gap-1.5">
            {index > 0 && (
              <span
                aria-hidden
                className={cn("h-px w-4", done ? "bg-emerald-400/60" : "bg-border")}
              />
            )}
            <span
              className={cn(
                "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium",
                done && "bg-emerald-500/10 text-emerald-300",
                current && "bg-secondary text-foreground",
                !done && !current && "text-muted-foreground",
              )}
            >
              {done && <Check aria-hidden className="size-3" />}
              {label === "Active" && liveVersion !== undefined && step === "live"
                ? `Active · v${liveVersion}`
                : label}
            </span>
          </li>
        );
      })}
    </ol>
  );
}

function activationTargetFromVersion(version: PolicyVersionSummary | undefined) {
  if (!version || !version.compiled_ok) return undefined;
  return { id: version.id, version_number: version.version_number };
}

function issueKey(issue: PolicyCompileIssue, index: number) {
  return `${issue.row}-${issue.col}-${issue.message}-${index}`;
}

function seedKey(document: PolicySnapshot, seedVersion: PolicyVersion | undefined) {
  return `${document.etag}:${document.id}:${seedVersion?.id ?? "template"}:${
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

  // The seed we are currently editing against. It only advances to a newer
  // server seed when doing so cannot discard unsaved work (see reconcile below),
  // so a background refetch never silently drops an admin's dirty draft.
  const [pinned, setPinned] = useState<PinnedSeed | null>(null);
  // Latest reported editor state, used only to decide whether an incoming seed
  // may replace the editor. PolicyEditorState is memoized, so reporting this
  // does not re-render the (heavy) editor subtree on every keystroke.
  const [editorState, setEditorState] = useState<EditorDirtyState | null>(null);

  if (!documentId) {
    return (
      <div className="surface-panel-subtle text-muted-foreground rounded-2xl p-6 text-sm">
        Select an override to edit its Rego source.
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

  const fresh: PinnedSeed = {
    document: document,
    documentId: document.id,
    key: seedKey(document, seedVersion),
    initialSource: seedVersion?.source ?? defaultPolicySource(document.domain),
    seedIsActive: seedVersion !== undefined && seedVersion.id === document.active_version_id,
    seedVersion,
  };

  // Adopting `fresh` remounts the editor and resets the draft to fresh.initialSource.
  // That is safe when the editor has no unsaved work relative to the pinned seed,
  // or when the draft already equals the incoming source (e.g. right after the
  // admin activated their own version — nothing is lost). Otherwise we keep the
  // pinned seed mounted and surface the newer version as a non-destructive notice.
  // Switching documents always adopts: the draft belongs to the previous
  // document and must never ride along into another document's editor.
  const sameDocument = pinned !== null && pinned.documentId === document.id;
  const editorDirty =
    sameDocument &&
    pinned !== null &&
    editorState !== null &&
    (editorState.draft !== pinned.initialSource || editorState.pending);
  const safeToAdopt = !editorDirty;
  const shouldAdopt = pinned === null || !sameDocument || (pinned.key !== fresh.key && safeToAdopt);
  const effective = shouldAdopt ? fresh : pinned;
  const keepingPinned = !shouldAdopt && pinned.key !== fresh.key;

  if (shouldAdopt && pinned?.key !== fresh.key) {
    // setState during render: React re-renders before commit; the guard above
    // makes it a fixed point (next render pinned.key === fresh.key). Clearing
    // the reported editor state keeps a stale report from the outgoing editor
    // from blocking or mislabeling the next seed until the new editor reports.
    setPinned(fresh);
    setEditorState(null);
  }

  return (
    <PolicyEditorState
      key={effective.key}
      document={effective.document}
      domains={domains}
      initialSource={effective.initialSource}
      seedIsActive={effective.seedIsActive}
      seedVersion={effective.seedVersion}
      versions={versionsQuery.data ?? []}
      onStateChange={setEditorState}
      newerSeed={
        keepingPinned
          ? { versionNumber: fresh.seedVersion?.version_number, onAdopt: () => setPinned(fresh) }
          : undefined
      }
    />
  );
}

const PolicyEditorState = memo(function PolicyEditorState({
  document,
  domains,
  initialSource,
  seedIsActive,
  seedVersion,
  versions,
  onStateChange,
  newerSeed,
}: PolicyEditorStateProps) {
  const [draft, setDraft] = useState(initialSource);
  const [captured, setCaptured] = useState(document);
  const [reviewRequired, setReviewRequired] = useState(false);
  const [applied, setApplied] = useState<PolicyApplyResult>();
  const [comment, setComment] = useState("");
  const [issues, setIssues] = useState<PolicyCompileIssue[]>([]);
  const [issueSource, setIssueSource] = useState(initialSource);
  const visibleIssues = issueSource === draft ? issues : [];
  const [validation, setValidation] = useState<ValidationState | null>(null);
  const [message, setMessage] = useState("");
  const [saved, setSaved] = useState<
    { source: string; target: ActivationTarget; compiled: boolean } | undefined
  >();
  const [confirmActivate, setConfirmActivate] = useState(false);
  const validatePolicy = useValidatePolicy();
  const createVersion = useCreatePolicyVersion();
  const activateVersion = useActivatePolicyVersion();

  // Report the draft plus whether there is uncommitted work (a saved-but-not-yet
  // activated version, or a typed comment) so the panel can decide whether an
  // incoming seed may safely replace this editor.
  const pending =
    Boolean(saved) ||
    comment.trim().length > 0 ||
    validatePolicy.isPending ||
    createVersion.isPending ||
    activateVersion.isPending;
  useEffect(() => {
    onStateChange({ draft, pending });
  }, [draft, pending, onStateChange]);

  const meta = policyDomainMeta(document.domain);
  const validated = validation?.source === draft && validation.result.compiled_ok;

  // An unedited seed from a compiled-but-inactive version is already "saved":
  // the remaining step is activation (e.g. after a page reload mid-flow).
  const seedSummary = versions.find((version) => version.id === seedVersion?.id);
  const savedTarget =
    saved?.source === draft && saved.compiled
      ? saved.target
      : !seedIsActive && draft === initialSource
        ? activationTargetFromVersion(seedSummary)
        : undefined;

  const liveInSync =
    (seedIsActive && draft === initialSource && !saved) ||
    Boolean(
      applied && saved?.source === draft && applied.document.active_version_id === saved.target.id,
    );
  const busy = validatePolicy.isPending || createVersion.isPending || activateVersion.isPending;
  const displayedDocument = applied ? { ...document, ...applied.document } : document;
  const activeVersionNumber =
    applied && saved && applied.document.active_version_id === saved.target.id
      ? saved.target.version_number
      : document.active_version?.version_number;
  const step: LifecycleStep = liveInSync
    ? "live"
    : savedTarget
      ? "activate"
      : validated
        ? "save"
        : "validate";

  async function validateDraft() {
    setMessage("");
    setIssues([]);
    const source = draft;
    setIssueSource(source);
    try {
      const result = await validatePolicy.mutateAsync({
        domain: document.domain,
        source,
      });
      setValidation({ source, result });
      setIssues(result.errors);
      setMessage(result.compiled_ok ? "Validation passed for the submitted source." : "");
    } catch (error) {
      const nextIssues = compileIssuesFromError(error);
      setIssues(nextIssues);
      setValidation({
        source,
        result: { compiled_ok: false, errors: nextIssues },
      });
      setMessage(nextIssues.length > 0 ? "" : messageFromError(error, "Validation failed."));
    }
  }

  async function saveVersion() {
    setMessage("");
    const source = draft;
    setIssueSource(source);
    const savedComment = comment;
    try {
      const result = await createVersion.mutateAsync({
        documentId: document.id,
        source,
        comment: savedComment,
      });
      setSaved({
        source,
        target: { id: result.id, version_number: result.version_number },
        compiled: result.compiled_ok,
      });
      setReviewRequired(true);
      if (!result.compiled_ok)
        setValidation({
          source,
          result: {
            compiled_ok: false,
            errors: [
              { row: 0, col: 0, message: result.compile_error ?? "Saved draft did not compile." },
            ],
          },
        });
      setApplied(undefined);
      setComment((current) => (current === savedComment ? "" : current));
      setIssues(
        result.compiled_ok
          ? []
          : [
              {
                row: 0,
                col: 0,
                message: result.compile_error ?? "This saved draft did not compile.",
              },
            ],
      );
      setMessage(
        result.compiled_ok
          ? `Saved v${result.version_number}. Review the current policy before activation.`
          : `Saved v${result.version_number}, but it did not compile. Its source is preserved in history; fix the diagnostics before saving another version.`,
      );
    } catch (error) {
      setIssues(compileIssuesFromError(error));
      setMessage(policyMutationMessage(error, "Unable to save policy version."));
    }
  }

  async function activateSavedVersion() {
    if (!savedTarget || reviewRequired) return;
    try {
      const result = await activateVersion.mutateAsync({
        documentId: document.id,
        version: savedTarget.id,
        etag: captured.etag,
      });
      setConfirmActivate(false);
      setApplied(result);
      setMessage(policyApplyMessage(result));
    } catch (error) {
      setReviewRequired(true);
      setConfirmActivate(false);
      setMessage(policyMutationMessage(error, "Unable to activate policy version."));
    }
  }

  const primaryAction = (() => {
    switch (step) {
      case "validate":
        return (
          <Button type="button" onClick={validateDraft} disabled={busy}>
            <CheckCircle2 className="size-4" />
            {validatePolicy.isPending ? "Validating..." : "Validate draft"}
          </Button>
        );
      case "save":
        return (
          <Button type="button" onClick={saveVersion} disabled={busy}>
            <Save className="size-4" />
            {createVersion.isPending ? "Saving..." : "Save as version"}
          </Button>
        );
      case "activate":
        return (
          <Button
            type="button"
            onClick={() => setConfirmActivate(true)}
            disabled={busy || reviewRequired}
          >
            <ShieldCheck className="size-4" />
            Activate v{savedTarget?.version_number}
          </Button>
        );
      case "live":
        return null;
    }
  })();

  return (
    <div className="space-y-5">
      <div className="surface-panel rounded-2xl border-0 p-4">
        <div className="flex flex-col justify-between gap-4 xl:flex-row xl:items-start">
          <div className="min-w-0 space-y-2">
            <div className="flex flex-wrap items-center gap-2.5">
              <h2 className="text-xl font-semibold tracking-tight">{document.name}</h2>
              <span className="text-muted-foreground text-sm">{meta.title}</span>
              <PolicyStatusPill
                status={policyDocumentStatus(displayedDocument)}
                versionNumber={activeVersionNumber}
              />
            </div>
            <LifecycleRail step={step} liveVersion={activeVersionNumber} />
          </div>

          <div className="flex shrink-0 flex-col items-stretch gap-2 sm:flex-row sm:items-center">
            {step !== "validate" && step !== "live" && (
              <Button type="button" variant="ghost" onClick={validateDraft} disabled={busy}>
                Re-validate
              </Button>
            )}
            {primaryAction}
          </div>
        </div>

        {step === "live" && (
          <p className="text-muted-foreground mt-3 text-sm">
            This source is selected as the active version in the saved document. Each server applies
            saved policy changes independently. Edit it to start a new draft.
          </p>
        )}
        {!document.enabled && (
          <p className="text-warning mt-3 text-sm">
            This override is disabled: the Silo baseline applies unchanged until it is re-enabled
            from the overrides list.
          </p>
        )}

        {newerSeed && (
          <div className="border-warning/40 bg-warning/10 mt-4 flex flex-col gap-2 rounded-lg border px-3 py-2 text-sm sm:flex-row sm:items-center sm:justify-between">
            <span className="text-warning">
              {newerSeed.versionNumber !== undefined
                ? `The saved policy changed (active version ${newerSeed.versionNumber}).`
                : "The saved policy revision changed."}{" "}
              Loading saved source replaces the text in this editor.
            </span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="shrink-0"
              onClick={newerSeed.onAdopt}
            >
              Load saved source
            </Button>
          </div>
        )}

        <PolicyRevisionReview
          documentId={document.id}
          disabled={busy}
          onAdopt={(snapshot) => {
            setCaptured(snapshot);
            setReviewRequired(false);
            setMessage("Current revision adopted. Your draft and saved version are unchanged.");
          }}
        />

        {step === "save" && (
          <div className="mt-4 max-w-lg">
            <Input
              value={comment}
              onChange={(event) => setComment(event.target.value)}
              placeholder="What changed? (optional, shown in history)"
              aria-label="Policy version comment"
            />
          </div>
        )}

        {message && (
          <div
            role="status"
            className={cn(
              "mt-4 rounded-lg border px-3 py-2 text-sm",
              applied &&
                (!applied.application.local_applied || applied.application.publication_failed)
                ? "border-warning/40 bg-warning/10 text-warning"
                : "border-border bg-secondary text-foreground",
            )}
          >
            {message}
          </div>
        )}

        {saved && saved.source !== draft && (
          <p className="text-muted-foreground text-sm">
            Current edits are not saved in v{saved.target.version_number}.
          </p>
        )}
        {validation && validation.source !== draft && (
          <p className="text-muted-foreground text-sm">
            The validation result applies to earlier source. Validate the current draft before
            saving.
          </p>
        )}
        {visibleIssues.length > 0 && (
          <div className="border-destructive/40 bg-destructive/10 text-destructive mt-4 rounded-lg border px-3 py-2">
            <h3 className="text-sm font-semibold">Compile issues</h3>
            <ul className="mt-2 space-y-1 text-xs">
              {visibleIssues.map((issue, index) => (
                <li key={issueKey(issue, index)}>
                  {issue.row > 0 ? `${issue.row}:${issue.col} ` : ""}
                  {issue.message}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>

      <RegoEditor value={draft} onChange={setDraft} issues={visibleIssues} height="520px" />

      <div className="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,1fr)_minmax(360px,0.7fr)]">
        <PolicySimulatePanel domains={domains} domain={document.domain} source={draft} />
        <PolicyVersionHistory
          documentId={document.id}
          activeVersionId={document.active_version_id ?? undefined}
        />
      </div>

      <AlertDialog open={confirmActivate} onOpenChange={setConfirmActivate}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Activate saved v{savedTarget?.version_number}?</AlertDialogTitle>
            <AlertDialogDescription>
              The reviewed policy is {captured.enabled ? "enabled" : "disabled"}, with active
              version {captured.active_version?.version_number ?? "none"}. This saves a new
              active-version selection. Servers reload independently; the result will report local
              application.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <Button onClick={() => void activateSavedVersion()} disabled={busy || reviewRequired}>
              <Play className="size-4" />
              Confirm activation
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
});
