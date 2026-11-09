import type { ReactNode } from "react";
import { useEffect, useState } from "react";
import { MoreHorizontal, MessageSquare, Pause, Play, Square, OctagonAlert } from "lucide-react";
import { toast } from "sonner";
import { StaleApiRequestContextError } from "@/api/client";
import type { AdminSession } from "@/api/types";
import {
  captureAdminPlaybackCommandAuthority,
  sendAdminPlaybackCommand,
  terminateAdminPlaybackSession,
  type AdminPlaybackCommandAction,
} from "@/api/v2/adminPlaybackCommands";
import { problemDisplayText, V2ProblemError } from "@/api/v2/request";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { getPrimaryPlaybackAction, isSessionPaused } from "./adminSessionActionModel";

type SessionActionKind = "pause" | "resume" | "stop" | "terminate" | "message";

interface AdminSessionActionsProps {
  session: AdminSession;
  compact?: boolean;
  layout?: "menu" | "inline";
  extraMenuItems?: ReactNode;
  inlinePrefixActions?: ReactNode;
  showInlineTerminate?: boolean;
}

const successMessages: Record<Exclude<SessionActionKind, "message">, MessageKey> = {
  pause: "web.admin.sessions.playback.toast.pauseSent",
  resume: "web.admin.sessions.playback.toast.resumeSent",
  stop: "web.admin.sessions.playback.toast.stopSent",
  terminate: "web.admin.sessions.playback.toast.terminateSent",
};

const fallbackMessages: Record<Exclude<SessionActionKind, "message">, MessageKey> = {
  pause: "web.admin.sessions.playback.toast.pauseFallback",
  resume: "web.admin.sessions.playback.toast.resumeFallback",
  stop: "web.admin.sessions.playback.toast.stopFallback",
  terminate: "web.admin.sessions.playback.toast.terminateFallback",
};

// Pause, resume, stop and message travel on the sequenced v2 commands: each
// click is a new command unless the last answer for the same command left its
// outcome unknown, which repeats it with the same identity so the server
// applies it once. A stale refusal means a newer command already won.
function describeTerminateReceipt(
  t: Translate,
  receipt: {
    authority_revoked: boolean;
    already_revoked: boolean;
    client_notified: boolean;
    durable_state: string;
  },
): string {
  return t("web.admin.sessions.playback.toast.terminateReceipt", {
    revoked: receipt.already_revoked ? "already" : receipt.authority_revoked ? "revoked" : "other",
    notified: receipt.client_notified ? "yes" : "other",
  });
}

// Problem text follows the problem-details rule: English shows the server's
// detail as before; another language shows the catalog's text for the field
// code or problem type when it has one.
function describeCommandFailure(
  t: Translate,
  action: AdminPlaybackCommandAction | "terminate",
  error: unknown,
): string {
  if (error instanceof StaleApiRequestContextError) {
    return t("web.admin.sessions.playback.failure.staleAuthority");
  }
  if (error instanceof V2ProblemError) {
    const { problem } = error;
    if (error.problemType === "conflict" && error.status === 409) {
      const reason = problem.detail ?? t("web.admin.sessions.playback.failure.conflictReason");
      return t("web.admin.sessions.playback.failure.notApplied", {
        action,
        reason: problemDisplayText(problem, reason),
      });
    }
    if (error.problemType === "not_found") return t("web.admin.sessions.playback.failure.ended");
    if (error.problemType === "dependency_unavailable") {
      const english = problem.detail || t("web.admin.sessions.playback.failure.unavailable");
      return problemDisplayText(problem, english);
    }
    return problemDisplayText(problem, problem.detail ?? error.message);
  }
  return error instanceof Error ? error.message : t("web.admin.sessions.playback.failure.generic");
}

export function AdminSessionActions({
  session,
  compact = false,
  layout = "menu",
  extraMenuItems,
  inlinePrefixActions,
  showInlineTerminate = false,
}: AdminSessionActionsProps) {
  const t = useT();
  const [pendingAction, setPendingAction] = useState<SessionActionKind | null>(null);
  const [messageOpen, setMessageOpen] = useState(false);
  const [message, setMessage] = useState("");
  const [optimisticPaused, setOptimisticPaused] = useState<boolean | null>(null);
  const supportsPlaybackControl = session.has_playback_control !== false;

  useEffect(() => {
    setOptimisticPaused(null);
  }, [session.session_id, session.is_paused]);

  useEffect(() => {
    if (!supportsPlaybackControl) {
      setMessageOpen(false);
    }
  }, [supportsPlaybackControl]);

  const paused = optimisticPaused ?? isSessionPaused(session);
  const primaryAction = paused
    ? { action: "resume" as const, label: t("web.admin.sessions.playback.action.resume") }
    : getPrimaryPlaybackAction(session);

  async function runAction(action: Exclude<SessionActionKind, "message">) {
    setPendingAction(action);
    try {
      const profileContext = captureAdminPlaybackCommandAuthority();
      if (action === "terminate") {
        // Terminate revokes authority first and reports client notification
        // separately; the toast shows both facts.
        const receipt = await terminateAdminPlaybackSession(
          session.session_id,
          undefined,
          profileContext,
        );
        toast.success(describeTerminateReceipt(t, receipt));
        return;
      }
      const receipt = await sendAdminPlaybackCommand(
        { sessionId: session.session_id, action },
        profileContext,
      );
      if (receipt.outcome === "replayed") {
        toast.success(
          t(
            receipt.delivery === "fallback_scheduled"
              ? "web.admin.sessions.playback.toast.replayedFallback"
              : "web.admin.sessions.playback.toast.replayed",
            { action },
          ),
        );
        return;
      }
      if (receipt.delivery === "dispatched" && action === "pause") {
        setOptimisticPaused(true);
      } else if (receipt.delivery === "dispatched" && action === "resume") {
        setOptimisticPaused(false);
      }
      toast.success(
        receipt.delivery === "fallback_scheduled"
          ? t(fallbackMessages[action])
          : t(successMessages[action]),
      );
    } catch (error) {
      toast.error(describeCommandFailure(t, action, error));
    } finally {
      setPendingAction(null);
    }
  }

  async function sendMessage() {
    const trimmed = message.trim();
    if (!trimmed) {
      toast.error(t("web.admin.sessions.playback.toast.messageRequired"));
      return;
    }

    setPendingAction("message");
    try {
      const profileContext = captureAdminPlaybackCommandAuthority();
      const receipt = await sendAdminPlaybackCommand(
        { sessionId: session.session_id, action: "message", message: trimmed },
        profileContext,
      );
      if (receipt.outcome === "replayed") {
        toast.success(t("web.admin.sessions.playback.toast.messageReplayed"));
        return;
      }
      toast.success(t("web.admin.sessions.playback.toast.messageSent"));
      setMessage("");
      setMessageOpen(false);
    } catch (error) {
      toast.error(describeCommandFailure(t, "message", error));
    } finally {
      setPendingAction(null);
    }
  }

  const terminateButton = showInlineTerminate ? (
    <Button
      variant="outline"
      size="sm"
      className={
        compact
          ? "border-destructive/40 text-destructive hover:bg-destructive/10 h-7 gap-1.5 px-2 text-[11px]"
          : "border-destructive/40 text-destructive hover:bg-destructive/10"
      }
      onClick={() => void runAction("terminate")}
      disabled={pendingAction !== null}
    >
      <OctagonAlert className="h-3.5 w-3.5" />
      {t("web.admin.sessions.playback.action.terminate")}
    </Button>
  ) : null;

  const menu = (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className={compact ? "h-7 w-7" : "h-8 w-8"}
          aria-label={t("web.admin.sessions.playback.menu.ariaLabel")}
          disabled={pendingAction !== null}
        >
          <MoreHorizontal className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuLabel>
          {layout === "inline"
            ? t("web.admin.sessions.playback.menu.titleSession")
            : supportsPlaybackControl
              ? t("web.admin.sessions.playback.menu.titlePlayback")
              : t("web.admin.sessions.playback.menu.titleLimited")}
        </DropdownMenuLabel>
        {supportsPlaybackControl ? (
          <DropdownMenuItem onSelect={() => void runAction(primaryAction.action)}>
            {primaryAction.action === "pause" ? (
              <Pause className="h-4 w-4" />
            ) : (
              <Play className="h-4 w-4" />
            )}
            {primaryAction.label}
          </DropdownMenuItem>
        ) : (
          <>
            <div className="text-muted-foreground px-2 py-1.5 text-xs leading-5">
              {t("web.admin.sessions.playback.unsupportedControl")}
            </div>
            <DropdownMenuSeparator />
          </>
        )}
        <DropdownMenuItem onSelect={() => void runAction("stop")}>
          <Square className="h-4 w-4" />
          {t("web.admin.sessions.playback.action.stop")}
        </DropdownMenuItem>
        {extraMenuItems ? (
          <>
            <DropdownMenuSeparator />
            {extraMenuItems}
          </>
        ) : null}
        {supportsPlaybackControl ? (
          <DropdownMenuItem onSelect={() => setMessageOpen(true)}>
            <MessageSquare className="h-4 w-4" />
            {t("web.admin.sessions.playback.action.message")}
          </DropdownMenuItem>
        ) : null}
        {showInlineTerminate ? null : (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={() => void runAction("terminate")}>
              <OctagonAlert className="h-4 w-4" />
              {t("web.admin.sessions.playback.action.terminate")}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );

  return (
    <>
      {layout === "inline" ? (
        <div className="flex flex-wrap items-center justify-end gap-1.5">
          {inlinePrefixActions}
          {terminateButton}
          {menu}
        </div>
      ) : (
        menu
      )}

      <Dialog
        open={supportsPlaybackControl && messageOpen}
        onOpenChange={(nextOpen) => setMessageOpen(supportsPlaybackControl && nextOpen)}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("web.admin.sessions.playback.messageDialog.title")}</DialogTitle>
            <DialogDescription>
              {t("web.admin.sessions.playback.messageDialog.description", {
                name:
                  session.username ||
                  t("web.admin.sessions.playback.messageDialog.userFallback", {
                    id: String(session.user_id),
                  }),
              })}
            </DialogDescription>
          </DialogHeader>
          <textarea
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            rows={4}
            placeholder={t("web.admin.sessions.playback.messageDialog.placeholder")}
            className="border-border bg-background text-foreground min-h-24 w-full resize-y rounded-md border px-3 py-2 text-sm outline-none focus:border-[var(--primary)]"
          />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setMessageOpen(false)}
              disabled={pendingAction === "message"}
            >
              {t("common.cancel")}
            </Button>
            <Button onClick={() => void sendMessage()} disabled={pendingAction === "message"}>
              {t("web.admin.sessions.playback.messageDialog.send")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
