import type { ReactNode } from "react";
import { useEffect, useState } from "react";
import { MoreHorizontal, MessageSquare, Pause, Play, Square, OctagonAlert } from "lucide-react";
import { toast } from "sonner";
import { StaleApiRequestContextError } from "@/api/client";
import type { AdminSession } from "@/api/types";
import {
  allocateAdminPlaybackCommand,
  captureAdminPlaybackCommandAuthority,
  sendAdminPlaybackCommand,
  terminateAdminPlaybackSession,
  type AdminPlaybackCommandAction,
} from "@/api/v2/adminPlaybackCommands";
import { V2ProblemError } from "@/api/v2/request";
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

const successMessages: Record<Exclude<SessionActionKind, "message">, string> = {
  pause: "Pause command sent",
  resume: "Resume command sent",
  stop: "Stop command sent",
  terminate: "Terminate command sent",
};

const fallbackMessages: Record<Exclude<SessionActionKind, "message">, string> = {
  pause: "Pause could not reach the player directly. Silo will end the session shortly instead.",
  resume: "Resume could not reach the player directly. Silo will end the session shortly instead.",
  stop: "Stop could not reach the player directly. Silo will end the session shortly instead.",
  terminate:
    "Terminate could not reach the player directly. Silo will end the session shortly instead.",
};

const unsupportedPlaybackControlCopy =
  "This session does not support live pause, resume, or messages. Stop and Terminate can still end playback.";

const staleAuthorityCopy = "Your session changed. Reload and try again.";

// Pause, resume, stop and message travel on the sequenced v2 commands: the
// identity is allocated once per click and the server applies it once, so a
// stale refusal means a newer command already won and the list should refresh.
function describeTerminateReceipt(receipt: {
  authority_revoked: boolean;
  already_revoked: boolean;
  client_notified: boolean;
  durable_state: string;
}): string {
  const revoked = receipt.already_revoked
    ? "Playback authority was already revoked"
    : receipt.authority_revoked
      ? "Playback authority revoked"
      : "Playback authority was not revoked";
  const notified = receipt.client_notified
    ? "the player was told to stop"
    : "the player could not be reached and will stop when it next contacts the server";
  return `${revoked}; ${notified}.`;
}

function describeCommandFailure(
  action: AdminPlaybackCommandAction | "terminate",
  error: unknown,
): string {
  if (error instanceof StaleApiRequestContextError) return staleAuthorityCopy;
  if (error instanceof V2ProblemError) {
    if (error.problemType === "conflict" && error.status === 409) {
      return `The ${action} command was not applied: ${error.problem.detail ?? "the session state changed"}`;
    }
    if (error.problemType === "not_found") return "This playback session has already ended.";
    if (error.problemType === "dependency_unavailable")
      return "Playback control is unavailable on this server.";
    return error.problem.detail ?? error.message;
  }
  return error instanceof Error ? error.message : "Failed to send session command";
}

export function AdminSessionActions({
  session,
  compact = false,
  layout = "menu",
  extraMenuItems,
  inlinePrefixActions,
  showInlineTerminate = false,
}: AdminSessionActionsProps) {
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
    ? { action: "resume" as const, label: "Resume" }
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
        toast.success(describeTerminateReceipt(receipt));
        return;
      }
      const identity = allocateAdminPlaybackCommand(session.session_id);
      const receipt = await sendAdminPlaybackCommand(
        { sessionId: session.session_id, action, identity },
        profileContext,
      );
      if (receipt.delivery === "dispatched" && action === "pause") {
        setOptimisticPaused(true);
      } else if (receipt.delivery === "dispatched" && action === "resume") {
        setOptimisticPaused(false);
      }
      toast.success(
        receipt.delivery === "fallback_scheduled"
          ? fallbackMessages[action]
          : successMessages[action],
      );
    } catch (error) {
      toast.error(describeCommandFailure(action, error));
    } finally {
      setPendingAction(null);
    }
  }

  async function sendMessage() {
    const trimmed = message.trim();
    if (!trimmed) {
      toast.error("Message is required");
      return;
    }

    setPendingAction("message");
    try {
      const profileContext = captureAdminPlaybackCommandAuthority();
      const identity = allocateAdminPlaybackCommand(session.session_id);
      await sendAdminPlaybackCommand(
        { sessionId: session.session_id, action: "message", identity, message: trimmed },
        profileContext,
      );
      toast.success("Message sent");
      setMessage("");
      setMessageOpen(false);
    } catch (error) {
      toast.error(describeCommandFailure("message", error));
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
      Terminate
    </Button>
  ) : null;

  const menu = (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className={compact ? "h-7 w-7" : "h-8 w-8"}
          aria-label="Session actions"
          disabled={pendingAction !== null}
        >
          <MoreHorizontal className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        {layout === "menu" ? (
          <>
            <DropdownMenuLabel>
              {supportsPlaybackControl ? "Playback Actions" : "Limited Playback Actions"}
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
                  {unsupportedPlaybackControlCopy}
                </div>
                <DropdownMenuSeparator />
              </>
            )}
            <DropdownMenuItem onSelect={() => void runAction("stop")}>
              <Square className="h-4 w-4" />
              Stop
            </DropdownMenuItem>
          </>
        ) : (
          <DropdownMenuLabel>Session Actions</DropdownMenuLabel>
        )}
        {extraMenuItems ? (
          <>
            {layout === "menu" ? <DropdownMenuSeparator /> : null}
            {extraMenuItems}
          </>
        ) : null}
        {supportsPlaybackControl ? (
          <DropdownMenuItem onSelect={() => setMessageOpen(true)}>
            <MessageSquare className="h-4 w-4" />
            Message…
          </DropdownMenuItem>
        ) : layout === "inline" ? (
          <>
            <div className="text-muted-foreground px-2 py-1.5 text-xs leading-5">
              {unsupportedPlaybackControlCopy}
            </div>
            <DropdownMenuSeparator />
          </>
        ) : null}
        {showInlineTerminate ? null : (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={() => void runAction("terminate")}>
              <OctagonAlert className="h-4 w-4" />
              Terminate
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );

  return (
    <>
      {layout === "inline" ? (
        <div className="flex items-center justify-end gap-1.5">
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
            <DialogTitle>Send Message</DialogTitle>
            <DialogDescription>
              Send a custom message to {session.username || `User #${session.user_id}`} during
              playback.
            </DialogDescription>
          </DialogHeader>
          <textarea
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            rows={4}
            placeholder="Server restart in 5 minutes. Please finish this episode soon."
            className="border-border bg-background text-foreground min-h-24 w-full resize-y rounded-md border px-3 py-2 text-sm outline-none focus:border-[var(--primary)]"
          />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setMessageOpen(false)}
              disabled={pendingAction === "message"}
            >
              Cancel
            </Button>
            <Button onClick={() => void sendMessage()} disabled={pendingAction === "message"}>
              Send Message
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
