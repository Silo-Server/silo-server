import { useId } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel?: string;
  /** Label for the dismiss button; change it when "Cancel" would read as the action. */
  cancelLabel?: string;
  variant?: "default" | "destructive";
  onConfirm: () => void;
  isPending?: boolean;
  /** Turns the confirm button off without a spinner, e.g. while `error` explains why. */
  confirmDisabled?: boolean;
  /** What the action does, one line each, under the description. */
  bullets?: { label: string; items: string[] };
  /** Says "This can't be undone." beside the buttons. */
  irreversible?: boolean;
  /** Where focus goes on close, when the control that opened it is gone (e.g. a menu item). */
  onCloseAutoFocus?: (event: Event) => void;
  /** Why the action didn't happen, shown in the dialog; the caller keeps it open. */
  error?: string | null;
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  variant = "default",
  onConfirm,
  isPending,
  confirmDisabled,
  bullets,
  irreversible = false,
  onCloseAutoFocus,
  error,
}: ConfirmDialogProps) {
  const id = useId();
  const items = bullets?.items ?? [];
  const itemId = (index: number) => `${id}-item-${index}`;
  const descriptionId = `${id}-description`;
  const noteId = `${id}-note`;
  // The list and the warning sit outside the description, so name them too:
  // a screen reader then reads every consequence when the dialog opens.
  const describedBy = [
    descriptionId,
    ...items.map((_, index) => itemId(index)),
    ...(irreversible ? [noteId] : []),
  ].join(" ");
  const buttons = (
    <>
      <AlertDialogCancel>{cancelLabel}</AlertDialogCancel>
      <AlertDialogAction
        onClick={onConfirm}
        variant={variant === "destructive" ? "destructive" : "default"}
        disabled={isPending || confirmDisabled}
      >
        {isPending ? (
          <>
            <span className="mr-2 inline-block h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent" />
            {confirmLabel}
          </>
        ) : (
          confirmLabel
        )}
      </AlertDialogAction>
    </>
  );
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent aria-describedby={describedBy} onCloseAutoFocus={onCloseAutoFocus}>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>
            <span id={descriptionId}>{description}</span>
          </AlertDialogDescription>
        </AlertDialogHeader>
        {items.length > 0 ? (
          <ul aria-label={bullets?.label} className="m-0 grid list-disc gap-1 pl-5 text-sm">
            {items.map((item, index) => (
              <li key={`${item}-${index}`} id={itemId(index)}>
                {item}
              </li>
            ))}
          </ul>
        ) : null}
        {error ? (
          <p role="alert" className="text-destructive text-sm font-medium">
            {error}
          </p>
        ) : null}
        {irreversible ? (
          <AlertDialogFooter className="items-center sm:justify-between">
            <p id={noteId} className="text-muted-foreground text-sm">
              This can&apos;t be undone.
            </p>
            <div className="flex gap-2">{buttons}</div>
          </AlertDialogFooter>
        ) : (
          <AlertDialogFooter>{buttons}</AlertDialogFooter>
        )}
      </AlertDialogContent>
    </AlertDialog>
  );
}
