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
import { getInitial } from "@/lib/text";

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
        <AvatarFallback>{getInitial(profile.name)}</AvatarFallback>
      </Avatar>
      <span className="flex min-w-0 flex-1 flex-col">
        {/* A cut name keeps its start in either script; w-fit keeps a short one
            on the page's start side. */}
        <span dir="auto" className="w-fit max-w-full truncate text-sm font-medium">
          {profile.name}
        </span>
        {/* contain-inline-size: the note added after a copy doesn't move CopyRow's button. */}
        <span className="text-muted-foreground text-xs contain-inline-size">
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
    // Wraps: when the name, or 8rem for the limit line beneath it, doesn't fit beside
    // the button, the button moves to the end of its own line, and its label wraps
    // when even that line is too narrow.
    <li className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md px-2 py-2">
      <span className="flex min-w-[min(8rem,100%)] flex-auto items-center gap-3">
        <ProfileLabel
          profile={profile}
          note={
            added === null
              ? undefined
              : t("web.profiles.allow.allowedTitles", { count: added, count_text: added })
          }
        />
      </span>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="ms-auto h-auto min-h-8 max-w-full py-1 text-center whitespace-normal"
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
