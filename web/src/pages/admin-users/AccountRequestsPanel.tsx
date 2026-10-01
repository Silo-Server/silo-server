import { useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight, TriangleAlert } from "lucide-react";
import { toast } from "sonner";

import type { AdminUser, RequestUserLimit } from "@/api/types";
import { isRequestEditorConflict } from "@/api/v2/adminRequests";
import { EditorConflict } from "@/components/admin/EditorConflict";
import { RequestLimitFields } from "@/components/admin/RequestLimitFields";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useRequestGroupLimit,
  useRequestSettings,
  useRequestUserLimit,
  useUpdateRequestUserLimit,
} from "@/hooks/queries/admin/requests";
import {
  clearLegacyRequestBlock,
  describeInheritedValue,
  describeRequestPolicy,
  effectiveRequestPolicy,
  formatRequestApproval,
  formatRequestQuota,
  hasRequestLimitErrors,
  isLegacyRequestBlock,
  requestGroupFor,
  requestLimitBody,
  requestLimitChanges,
  requestLimitDraft,
  requestLimitErrors,
  resolveRequestTerms,
  type RequestAccessGroup,
  type RequestLimitBody,
} from "@/lib/requestAccess";
import { useStagedDraft } from "@/pages/admin-settings/useStagedDraft";

/**
 * The Requests section of an account's page: what applies to it now and where
 * that comes from, and its own approval and limit. Its requests switch stays
 * with the rest of its access under Edit.
 */
export function AccountRequestsPanel({
  user,
  groupName,
}: {
  user: AdminUser;
  /** The name of the account's access group; undefined while unknown. */
  groupName: string | undefined;
}) {
  const settings = useRequestSettings();
  const accountLimit = useRequestUserLimit(user.id);
  const groupId = user.role === "admin" ? null : user.access_group_id;
  const groupLimit = useRequestGroupLimit(groupId);
  const updateLimit = useUpdateRequestUserLimit();
  const staged = useStagedDraft(accountLimit.data, requestLimitDraft, requestLimitChanges);
  const [conflict, setConflict] = useState(false);

  const group: RequestAccessGroup | null = requestGroupFor(
    user.role,
    user.access_group_id === null ? null : { name: groupName ?? "", limit: groupLimit.data },
  );
  const saved = accountLimit.data;
  const server = settings.data;
  const policy =
    saved && server
      ? effectiveRequestPolicy({
          role: user.role,
          requestsAllowed: user.effective_policy.requests_allowed,
          requestsAllowedOverride: user.requests_allowed,
          group,
          account: saved,
          server,
        })
      : undefined;

  // What "Use the default" means for this account: its group's setting, or
  // the server's when the group has none (or the account has no group).
  const inherited =
    server && (!group || group.limit)
      ? resolveRequestTerms(
          group?.limit ? [[{ kind: "group", name: group.name }, group.limit]] : [],
          server,
        )
      : undefined;
  const customSeed =
    inherited && !inherited.quota.unlimited
      ? { maxRequests: String(inherited.quota.max), windowDays: String(inherited.quota.days) }
      : server
        ? {
            maxRequests: String(server.global_max_requests),
            windowDays: String(server.global_window_days),
          }
        : { maxRequests: "", windowDays: "" };

  const draft = staged.draft;
  const errors = draft ? requestLimitErrors(draft) : {};
  const legacy = saved !== undefined && isLegacyRequestBlock(saved);
  const loadFailed = accountLimit.isError || settings.isError || groupLimit.isError;

  async function write(base: RequestUserLimit, body: RequestLimitBody) {
    try {
      staged.adopt(await updateLimit.mutateAsync({ userId: user.id, body: { ...base, ...body } }));
      setConflict(false);
    } catch (error) {
      if (isRequestEditorConflict(error)) setConflict(true);
    }
  }

  function save() {
    const { base } = staged;
    if (!base || !draft || staged.changes === 0 || conflict || hasRequestLimitErrors(errors)) {
      return;
    }
    void write(base, requestLimitBody(draft));
  }

  function resetLegacy() {
    if (!saved || conflict) return;
    void write(saved, clearLegacyRequestBlock(saved));
  }

  async function reload() {
    const result = await accountLimit.refetch();
    if (result.data && !result.isError) {
      staged.adopt(result.data);
      setConflict(false);
    } else {
      toast.error("Couldn't reload this account's request settings.");
    }
  }

  return (
    <section
      aria-labelledby="account-requests-heading"
      className="surface-panel overflow-hidden rounded-2xl border-0"
    >
      <div className="border-border flex flex-wrap items-start justify-between gap-3 border-b px-4 py-3">
        <div className="min-w-0 flex-1 basis-60">
          <h3 id="account-requests-heading" className="text-sm font-medium">
            Requests
          </h3>
          <p className="text-muted-foreground text-xs">
            How this account's requests are approved and how many it can make. To stop it
            requesting, turn off Media Requests under Edit › Access.
          </p>
        </div>
        <Button asChild variant="outline" size="sm">
          <Link to={`/admin/requests?user=${user.id}`}>
            Requests from this account
            <ArrowUpRight aria-hidden="true" className="size-3.5" />
          </Link>
        </Button>
      </div>

      <div className="space-y-4 px-4 py-4">
        {loadFailed ? (
          <div role="alert" className="flex flex-wrap items-center gap-2 text-sm">
            <span>Couldn't load this account's request settings.</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                void accountLimit.refetch();
                void settings.refetch();
                if (groupId !== null) void groupLimit.refetch();
              }}
            >
              Retry
            </Button>
          </div>
        ) : policy ? (
          <p className="text-sm" data-testid="request-policy-now">
            <span className="font-medium">Now:</span> {describeRequestPolicy(policy)}
          </p>
        ) : (
          <Skeleton className="h-5 w-3/4" />
        )}

        {/* A standing note, not an alert: it describes stored state, so a
            screen reader should not announce it on every load. */}
        {legacy ? (
          <div
            role="note"
            aria-label="Blocked by an old setting"
            className="space-y-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-sm"
          >
            <p className="flex items-center gap-2 font-medium">
              <TriangleAlert aria-hidden="true" className="size-4 text-amber-500" />
              Blocked by an old setting
            </p>
            <p className="text-muted-foreground text-xs leading-relaxed">
              This account's request limit still says it is blocked, a setting the editors no longer
              offer. Reset it to the default to use the approval and limit below. To keep the
              account from requesting, turn off Media Requests under Edit › Access.
            </p>
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={resetLegacy}
              disabled={updateLimit.isPending || conflict}
            >
              Reset to default
            </Button>
          </div>
        ) : draft ? (
          <RequestLimitFields
            draft={draft}
            onChange={(next) => staged.update(() => next)}
            inheritLabel={group ? "Use group default" : "Use server default"}
            inherited={
              inherited
                ? {
                    approval: describeInheritedValue(
                      formatRequestApproval(inherited.autoApprove),
                      inherited.approvalSource,
                      group,
                    ),
                    limit: describeInheritedValue(
                      formatRequestQuota(inherited.quota),
                      inherited.quotaSource,
                      group,
                    ),
                  }
                : undefined
            }
            customSeed={customSeed}
            errors={errors}
            disabled={updateLimit.isPending}
            // The section shares a column with the account details, too
            // narrow for the limit's numbers beside the approval.
            stacked
            subject="account"
          />
        ) : accountLimit.isLoading ? (
          <div className="grid gap-4">
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-16 w-full" />
          </div>
        ) : null}

        {user.role === "admin" ? (
          <p className="text-muted-foreground text-xs">
            Admin accounts don't use an access group's request settings.
          </p>
        ) : null}

        {conflict ? <EditorConflict onReload={reload} /> : null}

        {!legacy && draft ? (
          <div className="flex flex-wrap justify-end gap-2">
            {staged.changes > 0 ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => {
                  staged.reset();
                  setConflict(false);
                }}
                disabled={updateLimit.isPending}
              >
                Discard
              </Button>
            ) : null}
            <Button
              type="button"
              size="sm"
              onClick={save}
              disabled={
                staged.changes === 0 ||
                conflict ||
                updateLimit.isPending ||
                hasRequestLimitErrors(errors)
              }
            >
              {updateLimit.isPending ? "Saving..." : "Save"}
            </Button>
          </div>
        ) : null}
      </div>
    </section>
  );
}
