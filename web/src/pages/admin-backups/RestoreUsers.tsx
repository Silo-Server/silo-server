import { memo, useId, useState } from "react";
import { ShieldCheck } from "lucide-react";

import type { RestoreDataTypeId, RestoreItem, RestoreProfile } from "@/api/v2/adminBackupRestore";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";
import { DATA_TYPE_IDS, DATA_TYPE_LABELS, pendingSummary } from "@/lib/profileDataTypes";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { RowDisclosure } from "./Disclosure";
import { itemState } from "./restoreGroups";
import { StateTag, type ToggleKeys } from "./RestoreRows";
import { REASON_SENTENCES, policyDifferenceSentence, profileKey, toggleKeys } from "./restoreModel";

function droppedLibraries(count: number): string {
  return count === 1
    ? "1 library it could use isn't on this server and is dropped."
    : `${count} libraries it could use aren't on this server and are dropped.`;
}

function profileDetail(profile: RestoreProfile): string {
  const parts = [
    profile.is_primary ? "Primary" : null,
    profile.has_pin ? "Has a PIN" : null,
    profile.restricted ? "Restricted libraries" : null,
    profile.target === "new" ? "Created here" : "Merges into the profile here",
  ];
  return parts.filter(Boolean).join(" · ");
}

function ProfileRow({
  account,
  profile,
  checked,
  onToggle,
}: {
  account: RestoreItem;
  profile: RestoreProfile;
  checked: boolean;
  onToggle: ToggleKeys;
}) {
  const controlId = useId();
  const selectable = profile.selectable && account.selectable;
  // A choosable profile's detail line already says it merges or is created;
  // only a profile that can't be restored needs a sentence, saying why.
  // A matched profile with nothing to change is the normal state, not a caveat.
  const same = profile.status === "on_this_server" || profile.reason === "same";
  const why =
    profile.selectable || same
      ? null
      : profile.message || (profile.reason ? REASON_SENTENCES[profile.reason] : null);
  const detail = same
    ? "Matches this server"
    : [profileDetail(profile), pendingSummary(profile.data_types)].filter(Boolean).join(" · ");
  return (
    <li className="flex items-start gap-3 py-1.5">
      <Checkbox
        id={controlId}
        className="mt-0.5"
        checked={checked}
        disabled={!selectable}
        onCheckedChange={(value) => onToggle([profileKey(account.id, profile.id)], value === true)}
      />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-baseline gap-2">
          <Label
            htmlFor={controlId}
            className={cn("shrink-0 text-sm", !selectable && "text-muted-foreground")}
          >
            {profile.name}
          </Label>
          <span title={detail} className="text-muted-foreground min-w-0 truncate text-xs">
            {detail}
          </span>
        </div>
        {why ? (
          <SettingFieldStatus tone={profile.selectable ? "muted" : "warn"}>
            {why}
          </SettingFieldStatus>
        ) : null}
        {profile.no_library_access ? (
          <SettingFieldStatus tone="warn">
            None of its allowed libraries are on this server, so it&apos;s restored with no library
            access.
          </SettingFieldStatus>
        ) : profile.libraries_left_out > 0 ? (
          <SettingFieldStatus tone="warn">
            {droppedLibraries(profile.libraries_left_out)}
          </SettingFieldStatus>
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
  const controlId = useId();
  const detailsId = useId();
  const [open, setOpen] = useState(false);
  const account = item.account;
  if (!account) return null;
  const chosen = new Set(chosenProfiles ? chosenProfiles.split("\u0000") : []);
  const merges = account.target !== "new";
  const newProfiles = account.profiles.filter(
    (profile) => profile.selectable && profile.target === "new",
  ).length;
  const why =
    item.message || (!item.selectable && item.reason ? REASON_SENTENCES[item.reason] : null);
  // A matched account says what merging would add, then who it is.
  const pending =
    item.selectable && merges
      ? pendingSummary(
          account.profiles
            .filter((profile) => profile.selectable)
            .flatMap((profile) => profile.data_types),
        )
      : null;
  const contact = [
    pending,
    account.username !== item.label ? account.username : null,
    account.email || null,
  ]
    .filter(Boolean)
    .join(" · ");
  const status = !item.selectable
    ? null
    : merges
      ? `Merge into ${account.local_username || account.username}`
      : "New account";
  return (
    <li className="border-border/60 border-b last:border-b-0">
      <div className="flex min-h-10 items-center gap-3 py-1.5">
        <Checkbox
          id={controlId}
          checked={checked}
          disabled={!item.selectable}
          onCheckedChange={(value) => onToggle(toggleKeys("users", item), value === true)}
        />
        <div className="flex min-w-0 flex-1 items-baseline gap-2">
          <Label
            htmlFor={controlId}
            title={item.label}
            className={cn(
              "max-w-[45%] shrink-0 truncate text-sm font-medium",
              !item.selectable && "text-muted-foreground",
            )}
          >
            {item.label}
          </Label>
          {contact ? (
            <span title={contact} className="text-muted-foreground min-w-0 truncate text-xs">
              {contact}
            </span>
          ) : null}
        </div>
        {account.admin ? (
          <span className="text-muted-foreground hidden shrink-0 items-center gap-1 text-[11px] sm:inline-flex">
            <ShieldCheck aria-hidden="true" className="size-3.5" />
            Administrator
          </span>
        ) : null}
        {account.profiles.length > 0 ? (
          <span className="text-muted-foreground hidden shrink-0 text-[11px] whitespace-nowrap md:inline">
            {account.profiles.length} {account.profiles.length === 1 ? "profile" : "profiles"}
          </span>
        ) : null}
        {status ? (
          <span title={status} className="hidden max-w-40 shrink-0 truncate text-xs md:inline">
            {status}
          </span>
        ) : null}
        <StateTag state={itemState(item)} />
        <RowDisclosure
          open={open}
          onOpenChange={setOpen}
          controls={detailsId}
          label={open ? `Hide ${item.label}'s profiles` : `Show ${item.label}'s profiles`}
        />
      </div>
      {open ? (
        <div id={detailsId} className="space-y-1 pb-3 pl-7">
          {status ? <p className="text-[13px] md:hidden">{status}</p> : null}
          {account.admin ? (
            <p className="text-muted-foreground text-xs sm:hidden">Administrator</p>
          ) : null}
          {why ? (
            <SettingFieldStatus tone={item.selectable || item.reason === "same" ? "muted" : "warn"}>
              {why}
            </SettingFieldStatus>
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
            <SettingFieldStatus tone="warn">
              {droppedLibraries(account.libraries_left_out)}
            </SettingFieldStatus>
          ) : null}
          {item.selectable && newProfiles > account.profile_room ? (
            <SettingFieldStatus tone="warn">
              {account.profile_room === 0
                ? "This account here has no room for more profiles, so new ones are left out."
                : `This account here has room for ${account.profile_room} more ${account.profile_room === 1 ? "profile" : "profiles"}; any more are left out.`}
            </SettingFieldStatus>
          ) : null}
          {account.profiles.length > 0 ? (
            <ul aria-label={`Profiles of ${item.label}`}>
              {account.profiles.map((profile) => (
                <ProfileRow
                  key={profile.id}
                  account={item}
                  profile={profile}
                  checked={chosen.has(profile.id)}
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
  const baseId = useId();
  return (
    <div className="bg-background/60 my-2 rounded-lg border p-3">
      <fieldset className="space-y-2">
        <legend className="mb-2 text-[13px] font-medium">For the chosen profiles, restore:</legend>
        <div className="grid gap-x-4 gap-y-1.5 sm:grid-cols-2 xl:grid-cols-3">
          {DATA_TYPE_IDS.map((id) => {
            const controlId = `${baseId}-${id}`;
            const count = counts.get(id) ?? 0;
            return (
              <div key={id} className="flex items-center gap-2">
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
                <Label htmlFor={controlId} className="text-[13px] font-normal">
                  {DATA_TYPE_LABELS[id]}
                  {count > 0 ? (
                    <span className="text-muted-foreground tabular-nums"> ({count})</span>
                  ) : null}
                </Label>
              </div>
            );
          })}
        </div>
        <p className="text-muted-foreground text-xs leading-relaxed">
          Merging keeps whichever progress is newer and never deletes anything on this server. A new
          account keeps the password it had in the backup.
        </p>
      </fieldset>
    </div>
  );
}
