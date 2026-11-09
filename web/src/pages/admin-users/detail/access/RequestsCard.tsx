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
import { useT, type MessageKey, type Translate } from "@/i18n";
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
  inheritedFrom,
  inheritedValueText,
  rowChanged,
  rowDraft,
  rowOverride,
  rowSource,
  type RowDraft,
  type ValueSource,
} from "./policySources";
import { useAccountCardDraft } from "./useAccountCardDraft";

/** The space before a trailing note; kept out of the JSX so it is not read as text. */
const SPACE = " ";

const ACCOUNT: RequestPolicySource = { kind: "account" };

function requestSourceTag(source: RequestPolicySource): ValueSource {
  if (source.kind === "account") return "custom";
  return source.kind === "group" ? "group" : "default";
}

/** "50 per 7 days", "1 per day", or "Unlimited" ("unlimited" when `inline`, after a prefix). */
function formatRequestLimit(t: Translate, quota: RequestQuota, inline = false): string {
  if (quota.unlimited) {
    return inline
      ? t("web.admin.users.detail.access.inline.unlimited")
      : t("web.admin.users.detail.access.value.unlimited");
  }
  return t("web.admin.users.detail.access.requests.perWindow", {
    days: quota.days,
    days_text: String(quota.days),
    max: String(quota.max),
  });
}

function formatApprovalValue(t: Translate, autoApprove: boolean, inline = false): string {
  if (inline) {
    return autoApprove
      ? t("web.admin.users.detail.access.requests.automaticInline")
      : t("web.admin.users.detail.access.requests.needsApprovalInline");
  }
  return autoApprove
    ? t("web.admin.users.detail.access.requests.automatic")
    : t("web.admin.users.detail.access.requests.needsApproval");
}

const CAN_REQUEST_ROW = "web.admin.users.detail.access.requests.canRequest";
const APPROVAL_ROW = "web.admin.users.detail.access.requests.approval";
const LIMIT_ROW = "web.admin.users.detail.access.requests.limit";

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

/** "Default: …" or "Group: …" for a request term inherited from `source`. */
function inheritedRequestText(source: RequestPolicySource, value: string): string {
  return inheritedFrom(source.kind === "group" ? "group" : "default", value);
}

interface RequestsDraft {
  requests: RowDraft<boolean>;
  /** Undefined while the account's limit is not loaded. */
  limit: RequestLimitDraft | undefined;
}

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
  const t = useT();
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

  const draft = useAccountCardDraft<RequestsDraft, MessageKey>({
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
      const rows: MessageKey[] = [];
      if (rowChanged(d.requests, rowOverride(base.requests) ?? null)) {
        rows.push(CAN_REQUEST_ROW);
      }
      // The limit compares with the record the edit holds, not the live query.
      if (d.limit && limitRecordDraft) {
        if (d.limit.approval !== limitRecordDraft.approval) rows.push(APPROVAL_ROW);
        const limitOnly = { ...d.limit, approval: limitRecordDraft.approval };
        if (requestLimitChanges(limitOnly, limitRecordDraft) > 0) rows.push(LIMIT_ROW);
      }
      return rows;
    },
    validate: (d) =>
      d.limit && hasRequestLimitErrors(requestLimitErrors(d.limit))
        ? t("web.admin.users.detail.access.requests.fixLimit")
        : null,
    extra: {
      savedLabel: t("web.admin.users.detail.access.requests.savedPart"),
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
          throw new Error(t("web.admin.users.detail.access.requests.reloadFailed"));
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
        aria-label={t("web.admin.users.detail.access.requests.legacyTitle")}
        className="space-y-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-sm"
      >
        <p className="flex items-center gap-2 font-medium">
          <TriangleAlert aria-hidden="true" className="size-4 text-amber-500" />
          {t("web.admin.users.detail.access.requests.legacyTitle")}
        </p>
        <p className="text-muted-foreground text-xs leading-relaxed">
          {t("web.admin.users.detail.access.requests.legacyBody")}
        </p>
        {legacyConflict ? (
          <p className="text-xs">
            {t("web.admin.users.detail.access.requests.legacyConflict")}
            {SPACE}
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
              {t("web.admin.users.detail.access.requests.reload")}
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
            {t("web.admin.users.detail.access.requests.resetToDefault")}
          </Button>
        ) : null}
      </div>
    </div>
  ) : null;

  const serverOff =
    server && !server.requests_enabled ? (
      <p className="text-muted-foreground flex items-start gap-2 px-4 py-3 text-xs sm:px-5">
        <Info aria-hidden="true" className="text-info mt-px size-3.5 shrink-0" />
        {t("web.admin.users.detail.access.requests.serverOff")}
      </p>
    ) : null;

  const loadError = request.loadFailed ? (
    <div role="alert" className="flex flex-wrap items-center gap-2 px-4 py-3 text-sm sm:px-5">
      <span>{t("web.admin.users.detail.access.requests.loadFailed")}</span>
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
        {t("common.retry")}
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
          ? t("web.admin.users.detail.access.requests.adminDescription")
          : undefined
      }
      actions={
        <Button asChild variant="ghost" size="xs">
          <Link to={`/admin/requests?user=${user.id}`}>
            {t("web.admin.users.detail.access.requests.openRequests")}
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
            label={t(CAN_REQUEST_ROW)}
            row={d.requests}
            saved={draft.base?.requests_allowed ?? null}
            inherited={hints.requests_allowed}
            options={[
              { value: true, label: t("web.admin.users.detail.access.value.yes") },
              { value: false, label: t("web.admin.users.detail.access.value.no") },
            ]}
            onChange={(requests) => draft.setDraft((prev) => ({ ...prev, requests }))}
          />
          {legacyNote ??
            (d.limit ? (
              <div
                data-changed={
                  changed.has(APPROVAL_ROW) || changed.has(LIMIT_ROW) ? "true" : undefined
                }
                className="border-border/50 border-t px-4 py-3.5 sm:px-5"
              >
                <RequestLimitFields
                  draft={d.limit}
                  onChange={(limit) => draft.setDraft((prev) => ({ ...prev, limit }))}
                  inheritLabel={
                    group
                      ? t("web.admin.users.detail.access.requests.useGroupDefault")
                      : t("web.admin.users.detail.access.requests.useServerDefault")
                  }
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
            label={t(CAN_REQUEST_ROW)}
            value={
              user.effective_policy.requests_allowed
                ? t("web.admin.users.detail.access.value.yes")
                : t("web.admin.users.detail.access.value.no")
            }
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
                  label={t(APPROVAL_ROW)}
                  value={formatApprovalValue(t, terms.autoApprove)}
                  source={requestSourceTag(terms.approvalSource)}
                  base={
                    inherited
                      ? inheritedRequestText(
                          inherited.approvalSource,
                          formatApprovalValue(t, inherited.autoApprove, true),
                        )
                      : undefined
                  }
                />
                <PolicyValueRow
                  label={t(LIMIT_ROW)}
                  value={
                    <>
                      {formatRequestLimit(t, terms.quota)}
                      {used !== undefined && !terms.quota.unlimited ? (
                        <span className="text-muted-foreground text-xs font-normal">
                          {SPACE}
                          {t("web.admin.users.detail.access.requests.used", {
                            used: String(used),
                          })}
                        </span>
                      ) : null}
                    </>
                  }
                  source={requestSourceTag(terms.quotaSource)}
                  base={
                    inherited
                      ? inheritedRequestText(
                          inherited.quotaSource,
                          formatRequestLimit(t, inherited.quota, true),
                        )
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
