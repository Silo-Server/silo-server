import { ConfirmDialog } from "@/components/ConfirmDialog";
import { useT } from "@/i18n";

/** Shared confirmation for ending a watch party (lobby page + in-player panel). */
export function EndWatchPartyDialog({
  open,
  onOpenChange,
  onConfirm,
  isPending,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  isPending?: boolean;
}) {
  const t = useT();
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("web.watchTogether.endDialog.title")}
      description={t("web.watchTogether.endDialog.description")}
      confirmLabel={t("watchParty.end.confirm")}
      variant="destructive"
      onConfirm={onConfirm}
      isPending={isPending}
    />
  );
}
