import type { ReactNode } from "react";
import { useEffect, useState } from "react";
import { MoreHorizontal, MessageSquare, Pause, Play, Square, OctagonAlert } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { AdminSession } from "@/api/types";
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

type SessionCommandResponse = {
  command_id: string;
  status: string;
};

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
      const response = await api<SessionCommandResponse>(
        `/admin/sessions/${session.session_id}/${action}`,
        {
          method: "POST",
        },
      );
      if (response.status === "dispatched" && action === "pause") {
        setOptimisticPaused(true);
      } else if (response.status === "dispatched" && action === "resume") {
        setOptimisticPaused(false);
      }
      toast.success(
        response.status === "fallback_scheduled"
          ? fallbackMessages[action]
          : successMessages[action],
      );
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to send session command");
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
      await api<SessionCommandResponse>(`/admin/sessions/${session.session_id}/message`, {
        method: "POST",
        body: JSON.stringify({ message: trimmed }),
      });
      toast.success("Message sent");
      setMessage("");
      setMessageOpen(false);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to send message");
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
