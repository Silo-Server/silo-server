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
import { useT } from "@/i18n";

export function PolicyRevisionReview({
  documentId,
  onAdopt,
  disabled,
}: {
  documentId: string;
  onAdopt: (snapshot: PolicySnapshot) => void;
  disabled?: boolean;
}) {
  const t = useT();
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
      setError(err instanceof Error ? err.message : t("web.admin.access.policy.review.readFailed"));
    } finally {
      setLoading(false);
    }
  }
  return (
    <div className="flex flex-col gap-2 rounded-lg border p-3 text-sm">
      <Button variant="outline" onClick={() => void review()} disabled={disabled || loading}>
        {loading
          ? t("web.admin.access.policy.review.loading")
          : t("web.admin.access.policy.review.button")}
      </Button>
      {error && <p role="alert">{error}</p>}
      {candidate && (
        <div className="flex flex-col gap-2">
          <p>
            {t("web.admin.access.policy.review.summary", {
              name: candidate.name,
              state: candidate.enabled ? "enabled" : "disabled",
              has_version: candidate.active_version ? "yes" : "no",
              version: String(candidate.active_version?.version_number ?? ""),
            })}
          </p>
          <p>{t("web.admin.access.policy.review.keepNote")}</p>
          <Button
            className="h-auto whitespace-normal"
            disabled={disabled || loading}
            onClick={() => {
              onAdopt(candidate);
              setCandidate(undefined);
            }}
          >
            {t("web.admin.access.policy.review.adopt")}
          </Button>
        </div>
      )}
    </div>
  );
}

export function PolicyEnabledControl({ document }: { document: PolicyDocumentSummary }) {
  const t = useT();
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
      setMessage(
        err instanceof Error ? err.message : t("web.admin.access.policy.enabled.loadFailed"),
      );
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
      setMessage(policyMutationMessage(err, t("web.admin.access.policy.enabled.updateFailed")));
    }
  }
  return (
    <div className="flex flex-col gap-2 text-sm">
      {!intent && (
        <Button size="sm" variant="outline" onClick={() => void review()} disabled={loading}>
          {loading
            ? t("web.admin.access.policy.enabled.loading")
            : document.enabled
              ? t("web.admin.access.policy.enabled.disable")
              : t("web.admin.access.policy.enabled.enable")}
        </Button>
      )}
      {intent && (
        <div className="flex flex-col gap-2 rounded-lg border p-3">
          <p>
            {t("web.admin.access.policy.enabled.confirmText", {
              name: intent.snapshot.name,
              state: intent.snapshot.enabled ? "enabled" : "disabled",
              target: intent.enabled ? "enabled" : "disabled",
            })}
          </p>
          <Button onClick={() => void apply()} disabled={mutation.isPending || mutation.isError}>
            {t("web.admin.access.policy.enabled.confirm", {
              target: intent.enabled ? "enabled" : "disabled",
            })}
          </Button>
          <Button
            variant="ghost"
            onClick={() => setIntent(undefined)}
            disabled={mutation.isPending}
          >
            {t("common.cancel")}
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
