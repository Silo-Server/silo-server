import { memo, useId, useLayoutEffect, useRef, useState } from "react";
import { ShieldCheck } from "lucide-react";

import type { RestoreDataTypeId, RestoreItem, RestoreProfile } from "@/api/v2/adminBackupRestore";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { useT, type Translate } from "@/i18n";
import { cn } from "@/lib/utils";
import { DATA_TYPE_IDS, DATA_TYPE_LABELS, pendingSummary } from "@/lib/profileDataTypes";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { RowDisclosure } from "./Disclosure";
import { itemState } from "./restoreGroups";
import { BreakableKey, StateTag, type ToggleKeys } from "./RestoreRows";
import {
  REASON_SENTENCES,
  policyDifferenceSentence,
  profileKey,
  requiredPrimary,
  toggleKeys,
} from "./restoreModel";
import { useOverflowsInline } from "./useOverflowsInline";

function droppedLibraries(t: Translate, count: number): string {
  return t("web.admin.backups.restore.users.droppedLibraries", {
    count,
    count_text: count,
  });
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
  const nameRef = useRef<HTMLLabelElement>(null);
  const [nameOverflows, setNameOverflows] = useState(false);
  useLayoutEffect(() => {
    const name = nameRef.current;
    if (!name) return;
    // A name that fits can measure a fraction of a pixel wider than its box.
    const measure = () => setNameOverflows(name.scrollWidth > name.clientWidth + 1);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(name);
    return () => observer.disconnect();
  }, [profile.name]);
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
            row's width. A name that still overflows is clipped sideways at the row's end
            edge, and its title shows it in full. The start padding, offset by a negative
            margin, keeps a first letter whole whose ink reaches before its box, such as
            a leading "j". */}
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <Label
            ref={nameRef}
            htmlFor={controlId}
            title={profile.name}
            className={cn(
              "block min-w-0 text-sm wrap-anywhere",
              nameOverflows && "-ms-[0.25em] overflow-x-clip ps-[0.25em]",
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
  const [nameRef, nameOverflows] = useOverflowsInline<HTMLLabelElement>(item.label);
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
  const contact = [
    pending,
    account.username !== item.label ? account.username : null,
    account.email !== item.label ? account.email : null,
  ]
    .filter(Boolean)
    .join(" · ");
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
  // to fit: in the label, and in the merge status, which names the account it merges
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
          <Label
            ref={nameRef}
            htmlFor={controlId}
            title={item.label}
            className={cn(
              "block max-w-full shrink-0 text-sm font-medium wrap-anywhere",
              contact && "@2xl:max-w-max @2xl:flex-9 @2xl:truncate @2xl:[&_wbr]:hidden",
              nameOverflows && (contact ? "@max-2xl:overflow-x-clip" : "overflow-x-clip"),
              !item.selectable && "text-muted-foreground",
            )}
          >
            {/* <bdi> reads the name in its own direction, so an address that starts
                with digits stays in order on a right-to-left page. */}
            <bdi>
              <BreakableKey value={item.label} />
            </bdi>
          </Label>
          {contact ? (
            // Without the pending summary the line is the email (the label is the
            // username), which reads left to right in any language and so keeps its
            // start when cut short. With the summary first, the line follows the page.
            // In a narrow row, w-fit keeps a short line under the name, on the page's
            // start side; max-w-full stops a long one at the column's width so it is
            // still cut short.
            <span
              title={contact}
              dir={pending ? undefined : "ltr"}
              className="text-muted-foreground w-fit max-w-full min-w-0 truncate text-xs @2xl:max-w-max @2xl:flex-11"
            >
              {contact}
            </span>
          ) : null}
        </div>
        {account.admin ? (
          <span
            title={administrator}
            className="text-muted-foreground hidden shrink-0 items-center gap-1 text-[11px] @2xl:inline-flex"
          >
            <ShieldCheck aria-hidden="true" className="size-3.5" />
            <span className="sr-only @[52rem]:not-sr-only">{administrator}</span>
          </span>
        ) : null}
        {account.profiles.length > 0 ? (
          <span className="text-muted-foreground hidden shrink-0 text-[11px] whitespace-nowrap @[52rem]:inline">
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
                "text-[13px] wrap-anywhere",
                !merges && "@[52rem]:hidden",
                mergeOverflows && "overflow-x-clip",
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
                <div key={difference.field}>
                  <SettingFieldStatus tone="warn">
                    {policyDifferenceSentence(account.group, difference)}
                  </SettingFieldStatus>
                </div>
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
        <legend className="mb-2 text-[13px] font-medium">
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
