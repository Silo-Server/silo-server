import { useEffect, useRef } from "react";
import type { FormEvent, ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

interface StepFrameProps {
  title: string;
  /** One or two sentences on what this step decides and why it matters. */
  lede: ReactNode;
  children: ReactNode;
  /** Label for the primary action. Defaults to "Continue". */
  continueLabel?: string;
  /** Label while the primary action is in flight. */
  busyLabel?: string;
  busy?: boolean;
  disabled?: boolean;
  /** Render a "Skip for now" alongside the primary action. */
  onSkip?: () => void;
  /** When given the frame is a form and the primary action submits it. */
  onSubmit?: (event: FormEvent<HTMLFormElement>) => void;
  /** When given (and no `onSubmit`) the primary action is a plain button. */
  onContinue?: () => void;
  /** Extra content beside the actions, e.g. a note about where to change this later. */
  footnote?: ReactNode;
}

/**
 * The frame every step renders inside: heading, lede, the step's own rows,
 * and one action row at the bottom. Keeping the action row here is what makes
 * "Continue" mean the same thing on every screen.
 */
export function StepFrame({
  title,
  lede,
  children,
  continueLabel,
  busyLabel,
  busy = false,
  disabled = false,
  onSkip,
  onSubmit,
  onContinue,
  footnote,
}: StepFrameProps) {
  const t = useT();
  // Locking the fields drops focus from the one Enter was pressed in. When the
  // save fails and the step stays open, put the admin back where they were.
  const focusAtSubmit = useRef<Element | null>(null);
  useEffect(() => {
    if (busy) return;
    const target = focusAtSubmit.current;
    focusAtSubmit.current = null;
    if (
      target instanceof HTMLElement &&
      target.isConnected &&
      document.activeElement === document.body
    ) {
      target.focus();
    }
  }, [busy]);

  const actions = (
    <div className="setup-actions">
      <Button
        type={onSubmit ? "submit" : "button"}
        onClick={onSubmit ? undefined : onContinue}
        disabled={busy || disabled}
        className="min-w-32"
      >
        {busy
          ? (busyLabel ?? t("web.setup.wizard.saving"))
          : (continueLabel ?? t("web.ui.continue"))}
      </Button>
      {onSkip ? (
        <Button type="button" variant="ghost" onClick={onSkip} disabled={busy}>
          {t("web.ui.skipForNow")}
        </Button>
      ) : null}
      {footnote ? <p className="setup-footnote">{footnote}</p> : null}
    </div>
  );

  const body = (
    <>
      <header className="setup-step-header">
        <h1 className="setup-step-title">{title}</h1>
        <p className="setup-step-lede">{lede}</p>
      </header>
      <div className="setup-step-body">
        {/* Lock the fields while the step saves: an edit made after Continue
            would miss the saved batch and the step would advance past it. A
            disabled fieldset does not stop a Radix select from opening on
            pointerdown, so pointer input is cut off as well. */}
        <fieldset disabled={busy} className={cn("contents", busy && "pointer-events-none")}>
          {children}
        </fieldset>
      </div>
      {actions}
    </>
  );

  if (onSubmit) {
    return (
      <form
        onSubmit={(event) => {
          focusAtSubmit.current = document.activeElement;
          onSubmit(event);
        }}
        className="setup-step"
      >
        {body}
      </form>
    );
  }
  return <div className="setup-step">{body}</div>;
}

/** A hairline-ruled cluster of settings rows with a small heading above. */
export function StepSection({
  title,
  caption,
  children,
}: {
  title?: string;
  caption?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="setup-section">
      {title ? (
        <div className="setup-section-heading">
          <h2 className="setup-section-title">{title}</h2>
          {caption ? <p className="setup-section-caption">{caption}</p> : null}
        </div>
      ) : null}
      <div className="settings-field-list">{children}</div>
    </section>
  );
}

/** Loading placeholder shaped like a short stack of rows. */
export function StepSkeleton({ rows = 3 }: { rows?: number }) {
  const t = useT();
  return (
    <div className="setup-step" role="status" aria-label={t("common.loading")}>
      <div className="setup-step-header">
        <Skeleton className="h-8 w-56" />
        <Skeleton className="mt-3 h-4 w-80" />
      </div>
      <div className="setup-step-body space-y-4">
        {Array.from({ length: rows }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    </div>
  );
}
