import { useId } from "react";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
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
  allowanceDialogRows,
  allowanceRowState,
  describeMaturityLimit,
  useSetTitleAllowance,
  type TitleAllowanceProfile,
} from "@/hooks/queries/titleAllowances";
import { useT } from "@/i18n";

interface AllowTitleDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  itemId: string;
  itemTitle: string;
  profiles: TitleAllowanceProfile[];
}

/**
 * "Allow for…": the household's primary profile ticks the limited profiles
 * that may watch this title even though it is above their limit. A tick
 * saves at once.
 */
export default function AllowTitleDialog({
  open,
  onOpenChange,
  itemId,
  itemTitle,
  profiles,
}: AllowTitleDialogProps) {
  const t = useT();
  const { data: household } = useProfiles();
  const setAllowance = useSetTitleAllowance();
  const idPrefix = useId();
  const rows = allowanceDialogRows(profiles);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="gap-4 sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle>{t("web.profiles.allow.title", { name: itemTitle })}</DialogTitle>
          <DialogDescription>{t("web.profiles.allow.titleDescription")}</DialogDescription>
        </DialogHeader>
        {rows.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {t("web.profiles.allow.everyoneCanWatch")}
          </p>
        ) : (
          <ul className="flex flex-col gap-1">
            {rows.map((row) => {
              const { checked, disabled, note } = allowanceRowState(row.state);
              const avatar = household.find((p) => p.id === row.profile_id);
              const limit = describeMaturityLimit(row);
              const checkboxId = `${idPrefix}-${row.profile_id}`;
              return (
                <li key={row.profile_id}>
                  <label
                    htmlFor={checkboxId}
                    className="hover:bg-muted/60 flex items-center gap-3 rounded-md px-2 py-2 has-[button:disabled]:opacity-70"
                  >
                    <Checkbox
                      id={checkboxId}
                      checked={checked}
                      disabled={disabled || setAllowance.isPending}
                      onCheckedChange={(value) =>
                        setAllowance.mutate({
                          profileId: row.profile_id,
                          itemId,
                          allowed: value === true,
                        })
                      }
                    />
                    <Avatar className="size-8">
                      {avatar?.avatar_url ? <AvatarImage src={avatar.avatar_url} alt="" /> : null}
                      <AvatarFallback>{row.name.slice(0, 1).toUpperCase()}</AvatarFallback>
                    </Avatar>
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate text-sm font-medium">{row.name}</span>
                      <span className="text-muted-foreground text-xs">
                        {[limit && t("web.profiles.allow.limit", { limit }), note]
                          .filter(Boolean)
                          .join(" · ")}
                      </span>
                    </span>
                  </label>
                </li>
              );
            })}
          </ul>
        )}
      </DialogContent>
    </Dialog>
  );
}
