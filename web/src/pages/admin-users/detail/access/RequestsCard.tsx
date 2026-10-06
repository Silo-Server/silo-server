/* eslint-disable react-refresh/only-export-components -- the card and the request resolution it shares with Overview. */
import { useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight, Info, TriangleAlert } from "lucide-react";

import type { AdminUser, RequestUserLimit } from "@/api/types";
import { isRequestEditorConflict } from "@/api/v2/adminRequests";
import { RequestLimitFields } from "@/components/admin/RequestLimitFields";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useRequestGroupLimit,
  useRequestSettings,
  useRequestUserLimit,
  useUpdateRequestUserLimit,
} from "@/hooks/queries/admin/requests";
import { useAdminUserCapabilities } from "@/hooks/queries/admin/users";
import { useAdminUserRequestUsage } from "@/hooks/queries/admin/userActivity";
import {
  clearLegacyRequestBlock,
  describeInheritedValue,
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
  type RequestLimitDraft,
  type RequestLimitLayer,
  type RequestPolicySource,
  type RequestQuota,
  type ResolvedRequestTerms,
} from "@/lib/requestAccess";

import { useCardEditing } from "../cardEditing";
import { EditableCard, type AccessCardProps } from "./EditableCard";
import { ChoicePolicyEdit, PolicyValueRow } from "./PolicyRow";
import {
  inheritedValueText,
  rowChanged,
  rowDraft,
  rowOverride,
  rowSource,
  type RowDraft,
  type ValueSource,
} from "./policySources";
import { useAccountCardDraft } from "./useAccountCardDraft";

const ACCOUNT: RequestPolicySource = { kind: "account" };

function requestSourceTag(source: RequestPolicySource): ValueSource {
  if (source.kind === "account") return "custom";
  return source.kind === "group" ? "group" : "default";
}

/** "50 per 7 days", "1 per day", or "Unlimited". */
function formatRequestLimit(quota: RequestQuota): string {
  if (quota.unlimited) return "Unlimited";
  return `${quota.max} per ${quota.days === 1 ? "day" : `${quota.days} days`}`;
}

function formatApprovalValue(autoApprove: boolean): string {
  return autoApprove ? "Automatic" : "Needs approval";
}

/**
 * How requests resolve for an account: its own approval and limit, then its
 * access group's (none for an admin), then the server's settings.
 */
export function useAccountRequestTerms(user: AdminUser, groupName: string | undefined) {
  const settings = useRequestSettings();
  const accountLimit = useRequestUserLimit(user.id);
  const groupId = user.role === "admin" ? null : user.access_group_id;
  const groupLimit = useRequestGroupLimit(groupId);
  const group: RequestAccessGroup | null = requestGroupFor(
    user.role,
    user.access_group_id === null ? null : { name: groupName ?? "", limit: groupLimit.data },
  );
  const saved = accountLimit.data;
  const server = settings.data;
  const groupReady = !group || group.limit !== undefined;
  const groupLayers: Array<[RequestPolicySource, RequestLimitLayer]> = group?.limit
    ? [[{ kind: "group", name: group.name }, group.limit]]
    : [];
  /** What applies with the account's own layer on top. */
  const terms: ResolvedRequestTerms | undefined =
    saved && server && groupReady
      ? resolveRequestTerms([[ACCOUNT, saved], ...groupLayers], server)
      : undefined;
  /** What "Use the default" means for this account. */
  const inherited: ResolvedRequestTerms | undefined =
    server && groupReady ? resolveRequestTerms(groupLayers, server) : undefined;
  return {
    settings,
    accountLimit,
    groupLimit,
    groupId,
    group,
    saved,
    server,
    terms,
    inherited,
    legacy: saved !== undefined && isLegacyRequestBlock(saved),
    loadFailed: accountLimit.isError || settings.isError || groupLimit.isError,
  };
}

function inheritedPrefix(source: RequestPolicySource): string {
  return source.kind === "group" ? "Group" : "Default";
}

interface RequestsDraft {
  requests: RowDraft<boolean>;
  /** Undefined while the account's limit is not loaded. */
  limit: RequestLimitDraft | undefined;
}

const REQUEST_OPTIONS = [
  { value: true, label: "Yes" },
  { value: false, label: "No" },
];

export function RequestsCard({
  user,
  editor,
  manageable,
  available,
  groups,
  libraries,
  ctx,
  hints,
}: AccessCardProps) {
  const groupName = groups.find((group) => group.id === user.access_group_id)?.name;
  const request = useAccountRequestTerms(user, groupName);
  const { accountLimit, settings, groupLimit, group, terms, inherited, server } = request;
  const capabilities = useAdminUserCapabilities();
  const usage = useAdminUserRequestUsage(user.id, capabilities.data?.request_usage === true);
  const updateLimit = useUpdateRequestUserLimit();
  const { active } = useCardEditing();
  // The limit record an edit started from (validator included); a save or a
  // reload replaces it. Starting an edit sets it again.
  const [limitBase, setLimitBase] = useState<RequestUserLimit | undefined>(undefined);
  const [legacyConflict, setLegacyConflict] = useState(false);
  const limitRecord = limitBase ?? accountLimit.data;
  const limitRecordDraft = limitRecord ? requestLimitDraft(limitRecord) : undefined;

  const draft = useAccountCardDraft<RequestsDraft>({
    id: "requests",
    editor,
    toDraft: (account) => ({
      requests: rowDraft(account.requests_allowed),
      limit: accountLimit.data ? requestLimitDraft(accountLimit.data) : undefined,
    }),
    toBody: (d, base) => {
      const next = rowOverride(d.requests);
      return next !== undefined && rowChanged(d.requests, base.requests_allowed)
        ? { requests_allowed: next }
        : {};
    },
    changedRows: (d, base) => {
      const rows: string[] = [];
      if (rowChanged(d.requests, rowOverride(base.requests) ?? null)) {
        rows.push("Can request media");
      }
      // The limit compares with the record the edit holds, not the live query.
      if (d.limit && limitRecordDraft) {
        if (d.limit.approval !== limitRecordDraft.approval) rows.push("Approval");
        const limitOnly = { ...d.limit, approval: limitRecordDraft.approval };
        if (requestLimitChanges(limitOnly, limitRecordDraft) > 0) rows.push("Limit");
      }
      return rows;
    },
    validate: (d) =>
      d.limit && hasRequestLimitErrors(requestLimitErrors(d.limit))
        ? "Fix the request limit before saving."
        : null,
    extra: {
      savedLabel: "Request approval and limit",
      changed: (d) =>
        Boolean(d.limit && limitRecordDraft && requestLimitChanges(d.limit, limitRecordDraft) > 0),
      write: async (d) => {
        if (!limitRecord || !d.limit) return;
        const saved = await updateLimit.mutateAsync({
          userId: user.id,
          body: { ...limitRecord, ...requestLimitBody(d.limit) },
        });
        setLimitBase(saved);
      },
      reload: async () => {
        const result = await accountLimit.refetch();
        if (!result.data || result.isError) {
          throw new Error("Couldn't reload this account's request settings.");
        }
        setLimitBase(result.data);
        const oldLimit = limitRecordDraft;
        const freshLimit = requestLimitDraft(result.data);
        // The approval and the limit the admin left alone take the newer values.
        return (d: RequestsDraft) => {
          if (!d.limit || !oldLimit) return { ...d, limit: d.limit ?? freshLimit };
          const mine = d.limit;
          const limitKept =
            mine.limit === oldLimit.limit &&
            mine.maxRequests === oldLimit.maxRequests &&
            mine.windowDays === oldLimit.windowDays;
          return {
            ...d,
            limit: {
              approval: mine.approval === oldLimit.approval ? freshLimit.approval : mine.approval,
              limit: limitKept ? freshLimit.limit : mine.limit,
              maxRequests: limitKept ? freshLimit.maxRequests : mine.maxRequests,
              windowDays: limitKept ? freshLimit.windowDays : mine.windowDays,
            },
          };
        };
      },
    },
  });

  const d = draft.draft;
  const limitErrors = d?.limit ? requestLimitErrors(d.limit) : {};
  const changed = new Set(draft.changed);

  async function resetLegacy() {
    const saved = request.saved;
    if (!saved || legacyConflict || active !== null) return;
    try {
      await updateLimit.mutateAsync({
        userId: user.id,
        body: { ...saved, ...clearLegacyRequestBlock(saved) },
      });
    } catch (error) {
      if (isRequestEditorConflict(error)) setLegacyConflict(true);
    }
  }

  const legacyNote = request.legacy ? (
    // A standing note, not an alert: it describes stored state, so a screen
    // reader should not announce it on every load.
    <div className="px-4 py-3 sm:px-5">
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
          offer. Reset it to the default to use an approval and limit again. To keep the account
          from requesting, turn off Can request media instead.
        </p>
        {legacyConflict ? (
          <p className="text-xs">
            Another admin changed these request settings.{" "}
            <Button
              type="button"
              variant="link"
              size="xs"
              className="h-auto p-0"
              onClick={async () => {
                const result = await accountLimit.refetch();
                if (result.data && !result.isError) setLegacyConflict(false);
              }}
            >
              Reload
            </Button>
          </p>
        ) : null}
        {!draft.editing && manageable ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => void resetLegacy()}
            disabled={updateLimit.isPending || legacyConflict || active !== null || !available}
          >
            Reset to default
          </Button>
        ) : null}
      </div>
    </div>
  ) : null;

  const serverOff =
    server && !server.requests_enabled ? (
      <p className="text-muted-foreground flex items-start gap-2 px-4 py-3 text-xs sm:px-5">
        <Info aria-hidden="true" className="text-info mt-px size-3.5 shrink-0" />
        Requests are turned off for the whole server.
      </p>
    ) : null;

  const loadError = request.loadFailed ? (
    <div role="alert" className="flex flex-wrap items-center gap-2 px-4 py-3 text-sm sm:px-5">
      <span>Couldn't load this account's request settings.</span>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => {
          void accountLimit.refetch();
          void settings.refetch();
          if (request.groupId !== null) void groupLimit.refetch();
        }}
      >
        Retry
      </Button>
    </div>
  ) : null;

  // Custom starts from the inherited limit, else the server's global one.
  let customSeed = { maxRequests: "", windowDays: "" };
  if (inherited && !inherited.quota.unlimited) {
    customSeed = {
      maxRequests: String(inherited.quota.max),
      windowDays: String(inherited.quota.days),
    };
  } else if (server) {
    customSeed = {
      maxRequests: String(server.global_max_requests),
      windowDays: String(server.global_window_days),
    };
  }

  const used = usage.data && !usage.data.unlimited ? usage.data.used : undefined;

  return (
    <EditableCard
      id="requests"
      description={
        user.role === "admin"
          ? "Admin accounts don't use an access group's request settings."
          : undefined
      }
      actions={
        <Button asChild variant="ghost" size="xs">
          <Link to={`/admin/requests?user=${user.id}`}>
            Requests
            <ArrowUpRight aria-hidden="true" />
          </Link>
        </Button>
      }
      manageable={manageable}
      available={available}
      canEdit={editor !== undefined && accountLimit.data !== undefined && server !== undefined}
      invalid={hasRequestLimitErrors(limitErrors)}
      state={{
        ...draft,
        start: () => {
          setLimitBase(accountLimit.data);
          draft.start();
        },
      }}
    >
      {serverOff}
      {draft.editing && d ? (
        <>
          <ChoicePolicyEdit
            label="Can request media"
            row={d.requests}
            saved={draft.base?.requests_allowed ?? null}
            inherited={hints.requests_allowed}
            options={REQUEST_OPTIONS}
            onChange={(requests) => draft.setDraft((prev) => ({ ...prev, requests }))}
          />
          {legacyNote ??
            (d.limit ? (
              <div
                data-changed={changed.has("Approval") || changed.has("Limit") ? "true" : undefined}
                className="border-border/50 border-t px-4 py-3.5 sm:px-5"
              >
                <RequestLimitFields
                  draft={d.limit}
                  onChange={(limit) => draft.setDraft((prev) => ({ ...prev, limit }))}
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
                  errors={limitErrors}
                  disabled={draft.saving}
                  subject="account"
                />
              </div>
            ) : null)}
        </>
      ) : (
        <>
          <PolicyValueRow
            label="Can request media"
            value={user.effective_policy.requests_allowed ? "Yes" : "No"}
            source={rowSource(user, "requests", ctx)}
            base={inheritedValueText("requests", hints, ctx, libraries)}
          />
          {loadError ??
            legacyNote ??
            (!terms ? (
              <div className="space-y-2 px-4 py-3 sm:px-5">
                <Skeleton className="h-5 w-full" />
                <Skeleton className="h-5 w-full" />
              </div>
            ) : (
              <>
                <PolicyValueRow
                  label="Approval"
                  value={formatApprovalValue(terms.autoApprove)}
                  source={requestSourceTag(terms.approvalSource)}
                  base={
                    inherited
                      ? `${inheritedPrefix(inherited.approvalSource)}: ${formatApprovalValue(inherited.autoApprove).toLowerCase()}`
                      : undefined
                  }
                />
                <PolicyValueRow
                  label="Limit"
                  value={
                    <>
                      {formatRequestLimit(terms.quota)}
                      {used !== undefined && !terms.quota.unlimited ? (
                        <span className="text-muted-foreground text-xs font-normal">
                          {" "}
                          · {used} used
                        </span>
                      ) : null}
                    </>
                  }
                  source={requestSourceTag(terms.quotaSource)}
                  base={
                    inherited
                      ? `${inheritedPrefix(inherited.quotaSource)}: ${formatRequestLimit(inherited.quota).toLowerCase()}`
                      : undefined
                  }
                />
              </>
            ))}
        </>
      )}
      {draft.editing && request.loadFailed ? loadError : null}
    </EditableCard>
  );
}
