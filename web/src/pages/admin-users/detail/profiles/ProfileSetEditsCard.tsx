import { useState } from "react";

import type { AdminUser } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import {
  type AdminProfileSetEdit,
  useAdminUserProfileSetEdits,
  useClearAdminUserProfileSetEdit,
  useDismissAdminUserProfileSetEdit,
} from "@/hooks/queries/admin/userProfiles";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { formatDate } from "@/lib/datetime";
import { formatSettingValue, getSettingDefinition } from "@/lib/settingsDisplay";
import { settingLabel } from "@/lib/settingsText";
import { TOUCH_TARGETS } from "@/lib/touchTargets";

import { DetailCard } from "../ui";

const P = "web.admin.users.detail.profiles.setEdits";

/** The item fields a profile set carries, as the server names them. */
const FIELD_LABELS: Record<string, MessageKey> = {
  name: `${P}.field.name`,
  picture: `${P}.field.picture`,
  pin: `${P}.field.pin`,
  is_child: `${P}.field.isChild`,
  max_content_rating: `${P}.field.maxContentRating`,
  max_advisory_age: `${P}.field.maxAdvisoryAge`,
  require_advisory_age: `${P}.field.requireAdvisoryAge`,
  requests_blocked: `${P}.field.requests`,
};

const SETTING_PREFIX = "settings.";

/** Actionable records first: the administrator's own, then the person's. */
const KIND_ORDER: Record<string, number> = { override: 0, hidden: 1, unmerged: 2, settled: 3 };

function fieldLabel(t: Translate, field: string): string {
  if (field.startsWith(SETTING_PREFIX)) {
    const key = field.slice(SETTING_PREFIX.length);
    const definition = getSettingDefinition(key);
    return definition ? settingLabel(definition) : key;
  }
  const label = FIELD_LABELS[field];
  return label ? t(label) : field;
}

/** A record's value in force as the screen shows it. */
function valueText(t: Translate, edit: AdminProfileSetEdit): string {
  const value: unknown = edit.value;
  const field = edit.field ?? "";
  if (field.startsWith(SETTING_PREFIX)) {
    const key = field.slice(SETTING_PREFIX.length);
    const text =
      value === undefined
        ? t(`${P}.value.none`)
        : formatSettingValue(key, typeof value === "string" ? value : JSON.stringify(value));
    return edit.inherited ? t(`${P}.value.inherited`, { value: text }) : text;
  }
  switch (field) {
    case "pin":
      return value === "set" ? t(`${P}.value.pinSet`) : t(`${P}.value.pinNone`);
    case "picture":
      return value === "" ? t(`${P}.value.noPicture`) : t(`${P}.value.picture`);
    case "requests_blocked":
      return value === true ? t(`${P}.value.requestsBlocked`) : t(`${P}.value.requestsAllowed`);
    case "is_child":
    case "require_advisory_age":
      return value === true ? t(`${P}.value.on`) : t(`${P}.value.off`);
  }
  if (value === undefined || value === null || value === "") return t(`${P}.value.none`);
  return typeof value === "string" ? value : JSON.stringify(value);
}

type Pending =
  | { kind: "override"; edit: AdminProfileSetEdit }
  | { kind: "hidden"; edit: AdminProfileSetEdit };

type Notice = { hidden: boolean; profile: string; woken: boolean };

/**
 * Changes made on this server to profiles that come from the person's app.
 * The administrator's changes (a field they set, a profile they removed)
 * stay until cleared here; the person's own changes are listed with who made
 * them and when, since an override is not a lock. Hidden while there is
 * nothing to list.
 */
export function ProfileSetEditsCard({ user, manage }: { user: AdminUser; manage: boolean }) {
  const t = useT();
  const edits = useAdminUserProfileSetEdits(user.id, true);
  const clear = useClearAdminUserProfileSetEdit(user.id);
  const dismiss = useDismissAdminUserProfileSetEdit(user.id);
  const [pending, setPending] = useState<Pending | null>(null);
  const [notice, setNotice] = useState<Notice | null>(null);

  if (edits.isError) {
    return (
      <DetailCard title={t(`${P}.title`)}>
        <p role="alert" className="px-4 py-4 text-sm sm:px-5">
          {t(`${P}.loadFailed`)}
        </p>
      </DetailCard>
    );
  }
  const rows = [...(edits.data ?? [])].sort(
    (a, b) => (KIND_ORDER[a.kind] ?? 9) - (KIND_ORDER[b.kind] ?? 9),
  );
  if (rows.length === 0 && !notice) return null;

  const who = (edit: AdminProfileSetEdit) => edit.actor?.username ?? t(`${P}.deletedAccount`);
  const when = (edit: AdminProfileSetEdit) => formatDate(edit.at, "medium");

  const confirm = () => {
    if (!pending) return;
    const { edit } = pending;
    const target =
      pending.kind === "hidden"
        ? ({ kind: "hidden", syncId: edit.sync_id } as const)
        : ({
            kind: "override",
            profileId: edit.profile_id ?? "",
            field: edit.field ?? "",
          } as const);
    clear.mutate(target, {
      onSuccess: (result) => {
        setNotice({
          hidden: pending.kind === "hidden",
          profile: edit.profile_name,
          woken: result.wake_queued,
        });
        setPending(null);
      },
    });
  };

  return (
    <>
      <DetailCard
        title={t(`${P}.title`)}
        description={t(`${P}.description`, { name: user.username })}
      >
        {notice ? (
          <p role="status" className="border-border/50 border-b px-4 py-3 text-sm sm:px-5">
            {noticeText(t, notice, user.username)}
          </p>
        ) : null}
        {rows.map((edit) => (
          <EditRow
            key={`${edit.kind}:${edit.sync_id}:${edit.field ?? ""}`}
            edit={edit}
            manage={manage}
            who={who(edit)}
            when={when(edit)}
            dismissing={dismiss.isPending}
            onClear={() => {
              setNotice(null);
              setPending({ kind: edit.kind === "hidden" ? "hidden" : "override", edit });
            }}
            onDismiss={() =>
              dismiss.mutate({ profileId: edit.profile_id ?? "", field: edit.field ?? "" })
            }
          />
        ))}
      </DetailCard>
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(next) => (next ? undefined : setPending(null))}
        title={pending ? confirmTitle(t, pending) : ""}
        description={pending ? confirmDescription(t, pending) : ""}
        confirmLabel={pending?.kind === "hidden" ? t(`${P}.allowAgain`) : t(`${P}.clear`)}
        className={TOUCH_TARGETS}
        isPending={clear.isPending}
        onConfirm={confirm}
      />
    </>
  );
}

function noticeText(t: Translate, notice: Notice, name: string): string {
  if (notice.hidden) {
    return notice.woken
      ? t(`${P}.noticeHiddenWoken`, { profile: notice.profile, name })
      : t(`${P}.noticeHiddenNextSync`, { profile: notice.profile, name });
  }
  return notice.woken ? t(`${P}.noticeWoken`, { name }) : t(`${P}.noticeNextSync`, { name });
}

function confirmTitle(t: Translate, pending: Pending): string {
  const { edit } = pending;
  if (pending.kind === "hidden") return t(`${P}.allowTitle`, { profile: edit.profile_name });
  return t(`${P}.clearTitle`, {
    field: fieldLabel(t, edit.field ?? ""),
    profile: edit.profile_name,
  });
}

function confirmDescription(t: Translate, pending: Pending): string {
  const { edit } = pending;
  if (pending.kind === "hidden") return t(`${P}.allowDescription`, { profile: edit.profile_name });
  if (edit.field === "pin") return t(`${P}.clearPin`, { profile: edit.profile_name });
  if (edit.clear_applies) return t(`${P}.clearNow`, { profile: edit.profile_name });
  return t(`${P}.clearLater`, { field: fieldLabel(t, edit.field ?? "") });
}

function EditRow({
  edit,
  manage,
  who,
  when,
  dismissing,
  onClear,
  onDismiss,
}: {
  edit: AdminProfileSetEdit;
  manage: boolean;
  who: string;
  when: string;
  dismissing: boolean;
  onClear: () => void;
  onDismiss: () => void;
}) {
  const t = useT();
  const field = edit.field ? fieldLabel(t, edit.field) : "";
  let detail: string;
  switch (edit.kind) {
    case "override":
      detail = t(`${P}.setBy`, { name: who, date: when });
      break;
    case "hidden":
      detail = t(`${P}.hidden`, { name: who, date: when });
      break;
    case "settled":
      detail = t(`${P}.settled`, { name: who, date: when });
      break;
    default:
      detail = edit.field
        ? t(`${P}.changedBy`, { name: who, date: when })
        : t(`${P}.removedBy`, { name: who, date: when });
  }
  return (
    <div className="border-border/50 flex flex-wrap items-center gap-x-3 gap-y-2 border-t px-4 py-3 first:border-t-0 sm:px-5">
      <div className="min-w-0 flex-1 basis-48">
        <div dir="auto" className="text-sm font-medium break-words">
          {edit.profile_name}
        </div>
        {edit.field ? (
          <div className="text-sm break-words">
            {t(`${P}.fieldValue`, { field, value: valueText(t, edit) })}
          </div>
        ) : null}
        <div className="text-muted-foreground text-xs">
          {detail}
          {edit.kind === "unmerged" && edit.replaced_owner ? (
            <span className="ms-1">{t(`${P}.replacedOwner`)}</span>
          ) : null}
        </div>
      </div>
      {manage && (edit.kind === "override" || edit.kind === "hidden") ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label={
            edit.kind === "hidden"
              ? t(`${P}.allowAgainAria`, { profile: edit.profile_name })
              : t(`${P}.clearAria`, { field, profile: edit.profile_name })
          }
          onClick={onClear}
        >
          {edit.kind === "hidden" ? t(`${P}.allowAgain`) : t(`${P}.clear`)}
        </Button>
      ) : null}
      {manage && edit.kind === "settled" ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={dismissing}
          aria-label={t(`${P}.dismissAria`, { field, profile: edit.profile_name })}
          onClick={onDismiss}
        >
          {t(`${P}.dismiss`)}
        </Button>
      ) : null}
    </div>
  );
}
