import { useId, useState } from "react";
import type { Profile } from "@/api/types";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useProfiles } from "@/hooks/queries/profiles";
import {
  describeMaturityLimit,
  profileHasMaturityLimit,
  useAllowCollectionTitles,
  useAllowedCollections,
  useSetCollectionAllowance,
} from "@/hooks/queries/titleAllowances";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useT } from "@/i18n";

interface AllowCollectionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  collectionId: string;
  collectionName: string;
  /**
   * The collection can be allowed as it changes: a manual collection the
   * acting primary profile created. Otherwise its current titles are copied.
   */
  live: boolean;
}

/**
 * "Allow for…" on a personal collection: the household's limited profiles,
 * each either ticked to allow the collection as it changes, or offered a
 * one-time copy of the titles it holds now.
 */
export default function AllowCollectionDialog({
  open,
  onOpenChange,
  collectionId,
  collectionName,
  live,
}: AllowCollectionDialogProps) {
  const t = useT();
  const { data: household } = useProfiles({ enabled: open });
  const { profile: active } = useCurrentProfile();
  const limited = household.filter((p) => p.id !== active?.id && profileHasMaturityLimit(p));

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="gap-4 sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle>{t("web.profiles.allow.title", { name: collectionName })}</DialogTitle>
          <DialogDescription>
            {live ? t("web.profiles.allow.collectionLive") : t("web.profiles.allow.collectionCopy")}
          </DialogDescription>
        </DialogHeader>
        {limited.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {t("web.profiles.allow.noLimitedProfiles")}
          </p>
        ) : (
          <ul className="flex flex-col gap-1">
            {limited.map((profile) =>
              live ? (
                <LiveRow key={profile.id} profile={profile} collectionId={collectionId} />
              ) : (
                <CopyRow key={profile.id} profile={profile} collectionId={collectionId} />
              ),
            )}
          </ul>
        )}
      </DialogContent>
    </Dialog>
  );
}

function ProfileLabel({ profile, note }: { profile: Profile; note?: string }) {
  const t = useT();
  const limit = describeMaturityLimit(profile);
  return (
    <>
      <Avatar className="size-8">
        {profile.avatar_url ? <AvatarImage src={profile.avatar_url} alt="" /> : null}
        <AvatarFallback>{profile.name.slice(0, 1).toUpperCase()}</AvatarFallback>
      </Avatar>
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-sm font-medium">{profile.name}</span>
        <span className="text-muted-foreground text-xs">
          {[limit && t("web.profiles.allow.limit", { limit }), note].filter(Boolean).join(" · ")}
        </span>
      </span>
    </>
  );
}

function LiveRow({ profile, collectionId }: { profile: Profile; collectionId: string }) {
  const allowed = useAllowedCollections(profile.id, true);
  const setAllowance = useSetCollectionAllowance();
  const checkboxId = useId();
  const checked = (allowed.data ?? []).some((c) => c.collection_id === collectionId);
  return (
    <li>
      <label
        htmlFor={checkboxId}
        className="hover:bg-muted/60 flex items-center gap-3 rounded-md px-2 py-2"
      >
        <Checkbox
          id={checkboxId}
          checked={checked}
          disabled={allowed.isLoading || setAllowance.isPending}
          onCheckedChange={(value) =>
            setAllowance.mutate({ profileId: profile.id, collectionId, allowed: value === true })
          }
        />
        <ProfileLabel profile={profile} />
      </label>
    </li>
  );
}

function CopyRow({ profile, collectionId }: { profile: Profile; collectionId: string }) {
  const t = useT();
  const copy = useAllowCollectionTitles();
  const [added, setAdded] = useState<number | null>(null);
  return (
    <li className="flex items-center gap-3 rounded-md px-2 py-2">
      <ProfileLabel
        profile={profile}
        note={
          added === null
            ? undefined
            : t("web.profiles.allow.allowedTitles", { count: added, count_text: String(added) })
        }
      />
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={copy.isPending}
        onClick={() =>
          copy.mutate({ profileId: profile.id, collectionId }, { onSuccess: (n) => setAdded(n) })
        }
      >
        {t("web.profiles.allow.allowCurrent")}
      </Button>
    </li>
  );
}
