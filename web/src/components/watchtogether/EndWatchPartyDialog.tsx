import { ConfirmDialog } from "@/components/ConfirmDialog";

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
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title="End watch party?"
      description="End the watch party for everyone?"
      confirmLabel="End Party"
      variant="destructive"
      onConfirm={onConfirm}
      isPending={isPending}
    />
  );
}
