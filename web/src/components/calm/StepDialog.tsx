import { useState, type ReactNode, type Ref } from "react";
import { ChevronLeft, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

/** "Step 1 of 2 · note": the progress line for a footer. Phones show only the count. */
export function StepCount({ step, children }: { step: 1 | 2; children: string }) {
  return (
    <span className="text-muted-foreground inline-flex min-w-0 items-center gap-2 text-[13px]">
      <i aria-hidden className="bg-foreground h-1 w-[18px] shrink-0 rounded-full" />
      <i
        aria-hidden
        className={cn(
          "h-1 w-[18px] shrink-0 rounded-full",
          step === 2 ? "bg-foreground" : "bg-border",
        )}
      />
      <span className="shrink-0">Step {step} of 2</span>
      {/* Phones only fit the step count; screen readers still hear the note. */}
      <span aria-hidden className="max-sm:hidden">
        ·
      </span>
      <span className="truncate max-sm:sr-only">{children}</span>
    </span>
  );
}

/** Each open step dialog's content, to the element that opened it. */
const openers = new WeakMap<Element, HTMLElement | null>();

/**
 * The centered dialog of a two-step flow (pick, then fill in). At 1024px and
 * up it is centered; below, a bottom sheet, and the form step takes the whole
 * screen. `size` sets its shape: "picker" is the tall 1000px step, "form" the
 * 880px one, and "choice" a 1000px step only as tall as its few cards.
 *
 * It opens with no Radix trigger, so it keeps the element that had focus when
 * it mounted and gives focus back to it on close. When a link in one step
 * dialog swaps it for another, that element is gone by then, so focus goes
 * back to what opened the first dialog.
 */
export function StepDialog({
  size,
  onClose,
  onOpenFocus,
  back,
  title,
  titleRef,
  description,
  header,
  children,
  notice,
  footerStart = <span />,
  actions,
}: {
  size: "picker" | "form" | "choice";
  onClose: () => void;
  /** Puts focus where the step starts when the dialog opens. */
  onOpenFocus: () => void;
  /** A link back to the other step, above the title. */
  back?: { label: string; onClick: () => void } | null;
  title: string;
  titleRef?: Ref<HTMLHeadingElement>;
  description: ReactNode;
  /** Under the description, for a search field. */
  header?: ReactNode;
  children: ReactNode;
  /** Between the body and the footer, for a conflict or status line. */
  notice?: ReactNode;
  footerStart?: ReactNode;
  /** The footer's buttons after Cancel. */
  actions?: ReactNode;
}) {
  const [returnFocus] = useState(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const from = opener?.closest("[data-step-dialog]");
    return { opener, fallback: (from && openers.get(from)) || null };
  });
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent
        ref={(node) => {
          if (node) openers.set(node, returnFocus.opener);
        }}
        data-step-dialog=""
        showCloseButton={false}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          const { opener, fallback } = returnFocus;
          (opener?.isConnected ? opener : fallback)?.focus();
        }}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          onOpenFocus();
        }}
        className={cn(
          "flex flex-col gap-0 overflow-hidden rounded-[20px] p-0 sm:max-w-none",
          "max-lg:top-auto max-lg:bottom-0 max-lg:left-0 max-lg:w-full max-lg:max-w-none max-lg:translate-x-0 max-lg:translate-y-0 max-lg:rounded-b-none",
          size === "picker" &&
            "max-lg:h-[calc(100dvh-2.5rem)] max-lg:max-h-none lg:h-[min(860px,calc(100dvh-4rem))] lg:w-[min(1000px,calc(100vw-3rem))]",
          size === "form" &&
            "max-lg:h-dvh max-lg:max-h-none max-lg:rounded-none lg:w-[min(880px,calc(100vw-3rem))]",
          size === "choice" &&
            "max-lg:max-h-[calc(100dvh-2.5rem)] lg:w-[min(1000px,calc(100vw-3rem))]",
        )}
      >
        <div
          aria-hidden
          className="bg-muted-foreground/40 mx-auto mt-2.5 h-1 w-10 rounded-full lg:hidden"
        />
        <div className="grid gap-1.5 px-5 pt-5 pb-4 sm:px-7 sm:pt-6">
          {back ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="text-muted-foreground -ml-2.5 h-7 w-fit gap-1.5 px-2"
              onClick={back.onClick}
            >
              <ChevronLeft aria-hidden className="size-4" />
              {back.label}
            </Button>
          ) : null}
          <DialogTitle
            ref={titleRef}
            tabIndex={-1}
            className="pr-10 text-xl font-semibold tracking-[-0.02em] outline-none"
          >
            {title}
          </DialogTitle>
          <DialogDescription className="text-sm">{description}</DialogDescription>
          {header}
        </div>

        {children}

        {notice}

        <div className="border-border bg-surface/55 flex items-center justify-between gap-3 border-t px-5 py-4 max-lg:pb-[max(1rem,env(safe-area-inset-bottom))] sm:px-7">
          {footerStart}
          <div className="flex shrink-0 gap-2">
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            {actions}
          </div>
        </div>

        <DialogClose className="text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:ring-ring/50 absolute top-[18px] right-[18px] grid size-11 place-items-center rounded-[10px] outline-none focus-visible:ring-[3px] lg:size-[34px]">
          <X aria-hidden className="size-[18px]" />
          <span className="sr-only">Close</span>
        </DialogClose>
      </DialogContent>
    </Dialog>
  );
}
