import { cn } from "@/lib/utils";

export type Feedback = { tone: "ok" | "error"; text: string } | null;

/**
 * What the last action did. A polite live region always exists so a success
 * is announced; an error is an alert. Either one takes focus, because it
 * replaces the control the admin used.
 */
export function FeedbackLine({
  feedback,
  focusRef,
}: {
  feedback: Feedback;
  focusRef: React.RefObject<HTMLParagraphElement | null>;
}) {
  return (
    <div aria-live="polite">
      {feedback ? (
        <p
          ref={focusRef}
          tabIndex={-1}
          role={feedback.tone === "error" ? "alert" : undefined}
          className={cn(
            "py-1 text-sm focus:outline-none",
            feedback.tone === "error" ? "text-destructive" : "text-green-600 dark:text-green-400",
          )}
        >
          {feedback.text}
        </p>
      ) : null}
    </div>
  );
}
