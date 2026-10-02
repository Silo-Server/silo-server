import { useState } from "react";
import {
  fetchPolicySnapshot,
  policyApplyMessage,
  policyMutationMessage,
  type PolicyDocumentSummary,
  type PolicySnapshot,
} from "@/api/adminPolicy";
import { useSetPolicyDocumentEnabled } from "@/hooks/queries/admin/policy";
import { Button } from "@/components/ui/button";

export function PolicyRevisionReview({
  documentId,
  onAdopt,
  disabled,
}: {
  documentId: string;
  onAdopt: (snapshot: PolicySnapshot) => void;
  disabled?: boolean;
}) {
  const [candidate, setCandidate] = useState<PolicySnapshot>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  async function review() {
    setCandidate(undefined);
    setLoading(true);
    setError("");
    try {
      setCandidate(await fetchPolicySnapshot(documentId));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Unable to read current policy.");
    } finally {
      setLoading(false);
    }
  }
  return (
    <div className="flex flex-col gap-2 rounded-lg border p-3 text-sm">
      <Button variant="outline" onClick={() => void review()} disabled={disabled || loading}>
        {loading ? "Loading current policy..." : "Review current policy"}
      </Button>
      {error && <p role="alert">{error}</p>}
      {candidate && (
        <div className="flex flex-col gap-2">
          <p>
            {candidate.name}: {candidate.enabled ? "enabled" : "disabled"};{" "}
            {candidate.active_version
              ? `active version ${candidate.active_version.version_number}`
              : "no active version"}
            .
          </p>
          <p>
            Your draft and selected version will be kept. A later change by another administrator
            will still require a new review.
          </p>
          <Button
            className="h-auto whitespace-normal"
            disabled={disabled || loading}
            onClick={() => {
              onAdopt(candidate);
              setCandidate(undefined);
            }}
          >
            Use this revision and keep my draft
          </Button>
        </div>
      )}
    </div>
  );
}

export function PolicyEnabledControl({ document }: { document: PolicyDocumentSummary }) {
  const [intent, setIntent] = useState<{ snapshot: PolicySnapshot; enabled: boolean }>();
  const [loading, setLoading] = useState(false);
  const [message, setMessage] = useState("");
  const mutation = useSetPolicyDocumentEnabled();
  async function review() {
    setLoading(true);
    setMessage("");
    try {
      setIntent({ snapshot: await fetchPolicySnapshot(document.id), enabled: !document.enabled });
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "Unable to load policy.");
    } finally {
      setLoading(false);
    }
  }
  async function apply() {
    if (!intent) return;
    try {
      const result = await mutation.mutateAsync({
        documentId: intent.snapshot.id,
        enabled: intent.enabled,
        etag: intent.snapshot.etag,
      });
      setMessage(policyApplyMessage(result));
      setIntent(undefined);
    } catch (err) {
      setMessage(policyMutationMessage(err, "Unable to update policy."));
    }
  }
  return (
    <div className="flex flex-col gap-2 text-sm">
      {!intent && (
        <Button size="sm" variant="outline" onClick={() => void review()} disabled={loading}>
          {loading ? "Loading..." : document.enabled ? "Disable override" : "Enable override"}
        </Button>
      )}
      {intent && (
        <div className="flex flex-col gap-2 rounded-lg border p-3">
          <p>
            {intent.snapshot.name} is currently {intent.snapshot.enabled ? "enabled" : "disabled"}.
            Set it to {intent.enabled ? "enabled" : "disabled"}?
          </p>
          <Button onClick={() => void apply()} disabled={mutation.isPending || mutation.isError}>
            Confirm {intent.enabled ? "enable" : "disable"}
          </Button>
          <Button
            variant="ghost"
            onClick={() => setIntent(undefined)}
            disabled={mutation.isPending}
          >
            Cancel
          </Button>
          {mutation.isError && (
            <PolicyRevisionReview
              documentId={document.id}
              disabled={mutation.isPending}
              onAdopt={(snapshot) => {
                setIntent({ ...intent, snapshot });
                setMessage("");
                mutation.reset();
              }}
            />
          )}
        </div>
      )}
      {message && (
        <p role="status" className="max-w-lg">
          {message}
        </p>
      )}
    </div>
  );
}
