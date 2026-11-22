import { memo, useId, useState } from "react";
import { ShieldCheck } from "lucide-react";

import type { RestoreDataTypeId, RestoreItem, RestoreProfile } from "@/api/v2/adminBackupRestore";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import {
  NARROW_OVERFLOW_CLIP,
  OVERFLOW_CLIP,
  useOverflowsInline,
} from "@/hooks/useOverflowsInline";
import { useT, type Translate } from "@/i18n";
import { emailDir } from "@/lib/direction";
import { cn } from "@/lib/utils";
import { DATA_TYPE_IDS, DATA_TYPE_LABELS, pendingSummary } from "@/lib/profileDataTypes";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { RowDisclosure } from "./Disclosure";
import { itemState } from "./restoreGroups";
import { BreakableKey, StateTag, type ToggleKeys } from "./RestoreRows";
import {
  REASON_SENTENCES,
  nameDir,
  policyDifferenceSentence,
  profileKey,
  requiredPrimary,
  toggleKeys,
} from "./restoreModel";

const MIDDLE_DOT = "·";

function droppedLibraries(t: Translate, count: number): string {
  return t("web.admin.backups.restore.users.droppedLibraries", {
    count,
    count_text: count,
  });
}

/**
 * A policy difference, which quotes the access group's name as it is. The status stops at
 * the row's width and the sentence shrinks to it, so its words wrap there; a group name that
 * still overflows, such as one long run of joined emoji, is clipped sideways only while it
 * does, as the other restore names are.
 */
function PolicyDifferenceStatus({ sentence }: { sentence: string }) {
  const [ref, overflows] = useOverflowsInline<HTMLSpanElement>(sentence);
  return (
    <div className="*:max-w-full">
      <SettingFieldStatus tone="warn">
        <span ref={ref} className={cn("min-w-0", overflows && OVERFLOW_CLIP)}>
          {sentence}
        </span>
      </SettingFieldStatus>
    </div>
  );
}

function profileDetail(t: Translate, profile: RestoreProfile): string {
  const parts = [
    profile.is_primary ? t("web.admin.backups.restore.users.primary") : null,
    profile.has_pin ? t("web.admin.backups.restore.users.hasPin") : null,
    profile.restricted ? t("web.admin.backups.restore.users.restricted") : null,
    profile.target === "new"
      ? t("web.admin.backups.restore.users.createdHere")
      : t("web.admin.backups.restore.users.mergesHere"),
  ];
  return parts.filter(Boolean).join(" · ");
}

function ProfileRow({
  account,
  profile,
  checked,
  locked,
  primary,
  onToggle,
}: {
  account: RestoreItem;
  profile: RestoreProfile;
  checked: boolean;
  /** The required primary profile while another of the account's profiles is chosen. */
  locked: boolean;
  /** The account's required primary profile, chosen with this one. */
  primary: RestoreProfile | undefined;
  onToggle: ToggleKeys;
}) {
  const t = useT();
  const controlId = useId();
  const selectable = profile.selectable && account.selectable && !locked;
  const keys = (value: boolean) => {
    const own = profileKey(account.id, profile.id);
    return value && primary && primary.id !== profile.id
      ? [own, profileKey(account.id, primary.id)]
      : [own];
  };
  // A choosable profile's detail line already says it merges or is created;
  // only a profile that can't be restored needs a sentence, saying why.
  // A matched profile with nothing to change is the normal state, not a caveat.
  const same = profile.status === "on_this_server" || profile.reason === "same";
  const why =
    profile.selectable || same
      ? null
      : profile.message || (profile.reason ? t(REASON_SENTENCES[profile.reason]) : null);
  const detail = same
    ? t("web.admin.backups.restore.users.matches")
    : [profileDetail(t, profile), pendingSummary(profile.data_types)].filter(Boolean).join(" · ");
  // Only a name still wider than the row after wrapping is clipped: one with a run
  // no line break can split, such as many joined emoji. Clipping a name that fits
  // would trim letters whose ink reaches past their edge, such as a leading "j".
  const [nameRef, nameOverflows] = useOverflowsInline<HTMLLabelElement>(profile.name);
  return (
    <li className="flex items-start gap-3 py-1.5">
      <Checkbox
        id={controlId}
        className="mt-0.5"
        checked={checked || locked}
        disabled={!selectable}
        onCheckedChange={(value) => onToggle(keys(value === true), value === true)}
      />
      <div className="min-w-0 flex-1">
        {/* When the name and detail don't fit on one line, the detail moves under the
            name, and a name wider than the row wraps; as a block, its words wrap at the
            row's width. A name that still overflows is clipped sideways at its end edge,
            and its title shows it in full. It reads in its own direction, so the clipped
            end is the name's end in either script, and its lines line up with the
            page's. The start padding, offset by a negative margin, keeps a first letter
            whole whose ink reaches before its box, such as a leading "j". */}
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <Label
            ref={nameRef}
            htmlFor={controlId}
            title={profile.name}
            dir="auto"
            className={cn(
              "align-with-page block min-w-0 text-sm wrap-anywhere",
              nameOverflows && OVERFLOW_CLIP,
              !selectable && "text-muted-foreground",
            )}
          >
            {profile.name}
          </Label>
          <span title={detail} className="text-muted-foreground min-w-0 truncate text-xs">
            {detail}
          </span>
        </div>
        {/* Each status is a block of its own: SettingFieldStatus is inline, so
            two side by side would share a line. */}
        {why ? (
          <div>
            <SettingFieldStatus tone={profile.selectable ? "muted" : "warn"}>
              {why}
            </SettingFieldStatus>
          </div>
        ) : null}
        {locked ? (
          <div>
            <SettingFieldStatus tone="muted">
              {t("web.admin.backups.restore.users.lockedPrimary")}
            </SettingFieldStatus>
          </div>
        ) : null}
        {profile.no_library_access ? (
          <div>
            <SettingFieldStatus tone="warn">
              {t("web.admin.backups.restore.users.noLibraryAccess")}
            </SettingFieldStatus>
          </div>
        ) : profile.libraries_left_out > 0 ? (
          <div>
            <SettingFieldStatus tone="warn">
              {droppedLibraries(t, profile.libraries_left_out)}
            </SettingFieldStatus>
          </div>
        ) : null}
      </div>
    </li>
  );
}

/**
 * One account as a compact row: the account and its profiles open under the
 * chevron. `chosenProfiles` is the chosen profile ids joined, a string so
 * memo compares it cheaply.
 */
export const AccountRow = memo(function AccountRow({
  item,
  checked,
  chosenProfiles,
  onToggle,
}: {
  item: RestoreItem;
  checked: boolean;
  chosenProfiles: string;
  onToggle: ToggleKeys;
}) {
  const t = useT();
  const controlId = useId();
  const detailsId = useId();
  const [open, setOpen] = useState(false);
  const [nameRef, nameOverflows] = useOverflowsInline<HTMLSpanElement>(item.label);
  // The merge line exists only while the details are open, so opening them measures it.
  const [mergeRef, mergeOverflows] = useOverflowsInline<HTMLParagraphElement>(open && item);
  const account = item.account;
  if (!account) return null;
  const chosen = new Set(chosenProfiles ? chosenProfiles.split("\u0000") : []);
  const primary = requiredPrimary(item);
  const merges = account.target !== "new";
  const newProfiles = account.profiles.filter(
    (profile) => profile.selectable && profile.target === "new",
  ).length;
  const why =
    item.message || (!item.selectable && item.reason ? t(REASON_SENTENCES[item.reason]) : null);
  // A matched account says what merging would add, then who it is.
  const pending =
    item.selectable && merges
      ? pendingSummary(
          account.profiles
            .filter((profile) => profile.selectable)
            .flatMap((profile) => profile.data_types),
        )
      : null;
  // The label can already be the email: an invited account's username is its address,
  // and an account with no username is labeled by its email.
  const email = account.email && account.email !== item.label ? account.email : null;
  const hasContact = Boolean(pending || email);
  // With no username on either side, the name is the email the row is labelled with.
  const destination = account.local_username || account.username || item.label;
  const status = !item.selectable
    ? null
    : merges
      ? t("web.admin.backups.restore.users.mergeInto", { name: destination })
      : t("web.admin.backups.restore.users.newAccount");
  const administrator = t("web.admin.backups.restore.users.administrator");
  // Laid out like ItemRow (RestoreRows.tsx): a narrow row wraps the name above the
  // contact line, and a one-line row shares its width 9:11 between them, so the name
  // stops at 45% only when both are longer than their shares. The admin badge, profile
  // count and merge status would leave a 42rem row's name and contact about 220 px, so
  // below 52rem the row shows only the badge's icon, and the merge status only when it
  // names another account; the details show the admin role and the status. The badge
  // can cut a long merge destination short, so the details show a merge in full at
  // every width. As in ItemRow, a name clips sideways only while part of it can't wrap
  // to fit: in the name, and in the merge status, which names the account it merges
  // into.
  return (
    <li className="border-border/60 @container border-b last:border-b-0">
      <div className="flex min-h-10 items-center gap-3 py-1.5">
        {/* Named from the plain label: Chromium reads a <wbr> in it as a space. */}
        <Checkbox
          id={controlId}
          aria-label={item.label}
          checked={checked}
          disabled={!item.selectable}
          onCheckedChange={(value) => onToggle(toggleKeys("users", item), value === true)}
        />
        <div className="flex min-w-0 flex-1 flex-col gap-x-2 gap-y-0.5 @2xl:flex-row @2xl:items-baseline">
          {/* As in ItemRow: the label spans a narrow row's column, and the name inside
              reads in its own direction and is sized to its text. That also keeps an
              address that starts with digits in order on a right-to-left page. */}
          <Label
            htmlFor={controlId}
            title={item.label}
            className={cn(
              "block max-w-full shrink-0 text-sm font-medium wrap-anywhere",
              hasContact && "@2xl:max-w-max @2xl:min-w-0 @2xl:flex-9",
              !item.selectable && "text-muted-foreground",
            )}
          >
            <span
              ref={nameRef}
              dir={nameDir(item)}
              className={cn(
                "align-with-page block w-fit max-w-full",
                hasContact && "@2xl:truncate @2xl:[&_wbr]:hidden",
                nameOverflows && (hasContact ? NARROW_OVERFLOW_CLIP : OVERFLOW_CLIP),
              )}
            >
              <BreakableKey value={item.label} />
            </span>
          </Label>
          {hasContact ? (
            // The summary follows the page and the email takes its own direction, each cut
            // at its own end, so both keep their start: one cut line would lose the start
            // of whichever part sits at its end. Each part's tooltip is on the part, so it
            // reads in that part's direction too. When both don't fit on one line, the
            // email moves to its own, so neither is cut short unless it is wider than the
            // line on its own. As in UserDetailHeader, the separator sits before the email,
            // pulled back over the gap, so when the email opens a line the separator falls
            // past the clipped start edge. In a narrow row, w-fit keeps a short line under
            // the name, on the page's start side; max-w-full stops a long one at the
            // column's width.
            <span className="text-muted-foreground flex w-fit max-w-full min-w-0 flex-wrap gap-x-(--sep) overflow-x-clip text-xs [--sep:0.65em] @2xl:max-w-max @2xl:flex-11">
              {pending ? (
                <span title={pending} className="min-w-0 truncate">
                  {pending}
                </span>
              ) : null}
              {email ? (
                <span className={cn("flex min-w-0", pending && "-ms-(--sep)")}>
                  {pending ? (
                    <span aria-hidden="true" className="w-(--sep) shrink-0 text-center">
                      {MIDDLE_DOT}
                    </span>
                  ) : null}
                  <span dir={emailDir(email)} title={email} className="min-w-0 truncate">
                    {email}
                  </span>
                </span>
              ) : null}
            </span>
          ) : null}
        </div>
        {account.admin ? (
          <span
            title={administrator}
            className="text-muted-foreground hidden shrink-0 items-center gap-1 text-[0.6875rem] @2xl:inline-flex"
          >
            <ShieldCheck aria-hidden="true" className="size-3.5" />
            <span className="sr-only @[52rem]:not-sr-only">{administrator}</span>
          </span>
        ) : null}
        {account.profiles.length > 0 ? (
          <span className="text-muted-foreground hidden shrink-0 text-[0.6875rem] whitespace-nowrap @[52rem]:inline">
            {t("web.admin.backups.restore.users.profileCount", {
              count: account.profiles.length,
              count_text: account.profiles.length,
            })}
          </span>
        ) : null}
        {status ? (
          <span
            title={status}
            className={cn(
              "hidden max-w-40 shrink-0 truncate text-xs @[52rem]:inline",
              merges && destination !== item.label && "@2xl:inline",
            )}
          >
            {status}
          </span>
        ) : null}
        <StateTag state={itemState(item)} />
        <RowDisclosure
          open={open}
          onOpenChange={setOpen}
          controls={detailsId}
          label={t(
            open
              ? "web.admin.backups.restore.users.hideProfiles"
              : "web.admin.backups.restore.users.showProfiles",
            { name: item.label },
          )}
        />
      </div>
      {open ? (
        <div id={detailsId} className="space-y-1 ps-7 pb-3">
          {status ? (
            <p
              ref={mergeRef}
              title={status}
              className={cn(
                "text-[0.8125rem] wrap-anywhere",
                !merges && "@[52rem]:hidden",
                mergeOverflows && OVERFLOW_CLIP,
              )}
            >
              {status}
            </p>
          ) : null}
          {account.admin ? (
            <p className="text-muted-foreground text-xs @[52rem]:hidden">{administrator}</p>
          ) : null}
          {/* One block per status, as in ProfileRow. */}
          {why ? (
            <div>
              <SettingFieldStatus
                tone={item.selectable || item.reason === "same" ? "muted" : "warn"}
              >
                {why}
              </SettingFieldStatus>
            </div>
          ) : null}
          {!item.selectable
            ? account.policy_differences.map((difference) => (
                <PolicyDifferenceStatus
                  key={difference.field}
                  sentence={policyDifferenceSentence(account.group, difference)}
                />
              ))
            : null}
          {account.libraries_left_out > 0 ? (
            <div>
              <SettingFieldStatus tone="warn">
                {droppedLibraries(t, account.libraries_left_out)}
              </SettingFieldStatus>
            </div>
          ) : null}
          {item.selectable && newProfiles > account.profile_room ? (
            <div>
              <SettingFieldStatus tone="warn">
                {account.profile_room === 0
                  ? t("web.admin.backups.restore.users.noRoom")
                  : t("web.admin.backups.restore.users.room", {
                      count: account.profile_room,
                      count_text: account.profile_room,
                    })}
              </SettingFieldStatus>
            </div>
          ) : null}
          {account.profiles.length > 0 ? (
            <ul aria-label={t("web.admin.backups.restore.users.profilesOf", { name: item.label })}>
              {account.profiles.map((profile) => (
                <ProfileRow
                  key={profile.id}
                  account={item}
                  profile={profile}
                  checked={chosen.has(profile.id)}
                  locked={profile.id === primary?.id && [...chosen].some((id) => id !== primary.id)}
                  primary={primary}
                  onToggle={onToggle}
                />
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </li>
  );
});

/** "For the chosen profiles, restore:" with one checkbox per data type, as a compact block. */
export function DataTypesPanel({
  dataTypes,
  onChange,
  counts,
}: {
  dataTypes: ReadonlySet<RestoreDataTypeId>;
  onChange: (dataTypes: Set<RestoreDataTypeId>) => void;
  /** Entries of each type across the chosen profiles. */
  counts: ReadonlyMap<RestoreDataTypeId, number>;
}) {
  const t = useT();
  const baseId = useId();
  return (
    <div className="bg-background/60 my-2 rounded-lg border p-3">
      <fieldset className="space-y-2">
        <legend className="mb-2 text-[0.8125rem] font-medium">
          {t("web.admin.backups.restore.users.dataLegend")}
        </legend>
        <div className="grid gap-x-4 gap-y-1.5 sm:grid-cols-2 xl:grid-cols-3">
          {DATA_TYPE_IDS.map((id) => {
            const controlId = `${baseId}-${id}`;
            const count = counts.get(id) ?? 0;
            // The checkbox's box is one line tall. The label's text size replaces Label's
            // leading-none, so it inherits the row's unitless line height. The text is in rem
            // like the checkbox, so a larger browser font grows both and the line stays
            // taller than the checkbox, and a minimum font size still spaces wrapped lines.
            // The label is a block so its count wraps with its text, and text-pretty keeps
            // the count off a last line of its own where the browser supports it.
            return (
              <div key={id} className="flex items-start gap-2 text-[0.8125rem] leading-normal">
                <span className="flex min-h-lh shrink-0 items-center">
                  <Checkbox
                    id={controlId}
                    checked={dataTypes.has(id)}
                    onCheckedChange={(value) => {
                      const next = new Set(dataTypes);
                      if (value === true) next.add(id);
                      else next.delete(id);
                      onChange(next);
                    }}
                  />
                </span>
                <Label
                  htmlFor={controlId}
                  className="block text-[0.8125rem] font-normal text-pretty"
                >
                  {count > 0
                    ? t("web.admin.backups.restore.users.dataTypeCount", {
                        label: DATA_TYPE_LABELS[id],
                        count: count,
                        muted: (chunks) => (
                          <span className="text-muted-foreground tabular-nums">{chunks}</span>
                        ),
                      })
                    : DATA_TYPE_LABELS[id]}
                </Label>
              </div>
            );
          })}
        </div>
        <p className="text-muted-foreground text-xs leading-relaxed">
          {t("web.admin.backups.restore.users.mergeNote")}
        </p>
      </fieldset>
    </div>
  );
}
