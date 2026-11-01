import { useId, useRef, useState, type DragEvent } from "react";
import { FileUp, LoaderCircle } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";

import { captureProfileRequestContext, type ProfileRequestContextSnapshot } from "@/api/client";
import { captureAdminAuthority } from "@/api/v2/adminAuthority";
import {
  completeBackupUpload,
  createBackupUpload,
  deleteBackupUpload,
  putBackupUploadPart,
  type AdminBackupUpload,
} from "@/api/v2/adminBackupUpload";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Progress } from "@/components/ui/progress";
import { backupUploadsKey } from "@/hooks/queries/admin/backupUploads";
import { cn } from "@/lib/utils";

import { uploadRestorePath } from "./backupPresentation";
import {
  failureCopy,
  failureOfError,
  openedSentence,
  PartFailedError,
  progressLabel,
  progressPercent,
  retryable,
  sendParts,
  sniffRestoreSet,
  type UploadFailure,
} from "./uploadModel";

type Phase =
  | { step: "choose" }
  | { step: "uploading"; upload: AdminBackupUpload | null }
  | { step: "checking"; upload: AdminBackupUpload }
  | { step: "opened"; upload: AdminBackupUpload }
  | {
      step: "failed";
      failure: UploadFailure;
      /** The upload to continue, for a failure that can be retried. */
      upload: AdminBackupUpload | null;
      /** The part to continue from; the parts before it arrived. */
      from: number;
    };

/** The first bytes a sniff reads: one tar header. */
const SNIFF_BYTES = 512;

export interface UploadBackupDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The largest file an upload takes, from the capabilities. */
  maxBytes: number;
}

/**
 * "Upload a backup": choose a restore set, send it in parts with progress,
 * continue from the failed part on "Try again", and once the server checked
 * it, go on to choose what to restore.
 */
export function UploadBackupDialog({ open, onOpenChange, maxBytes }: UploadBackupDialogProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const inputRef = useRef<HTMLInputElement>(null);
  const descriptionId = useId();
  const [phase, setPhase] = useState<Phase>({ step: "choose" });
  const [dragging, setDragging] = useState(false);
  // The ref is what the running upload reads; the state is what renders.
  const file = useRef<File | null>(null);
  const [chosenFile, setChosenFile] = useState<{ name: string; size: number } | null>(null);
  const authority = useRef<ProfileRequestContextSnapshot | null>(null);
  const abort = useRef<AbortController | null>(null);

  const refreshUploads = () => void queryClient.invalidateQueries({ queryKey: backupUploadsKey() });

  // A closed dialog starts over next time; whatever it was sending stops.
  const close = () => {
    abort.current?.abort();
    setPhase({ step: "choose" });
    setDragging(false);
    onOpenChange(false);
  };

  const fail = (failure: UploadFailure, upload: AdminBackupUpload | null = null, from = 0) =>
    setPhase({ step: "failed", failure, upload, from });

  /** Sends parts from `from`, then completes; every step reports where it stopped. */
  const run = async (upload: AdminBackupUpload, from: number) => {
    const chosen = file.current;
    const context = authority.current;
    if (!chosen || !context) return;
    const controller = new AbortController();
    abort.current = controller;
    const limits = { sizeBytes: chosen.size, limitBytes: maxBytes };
    setPhase({ step: "uploading", upload });
    let sent = upload;
    try {
      sent = await sendParts({
        upload,
        file: chosen,
        from,
        signal: controller.signal,
        put: (part, bytes) =>
          putBackupUploadPart(context, upload.id, part, bytes, controller.signal),
        onProgress: (progress) => setPhase({ step: "uploading", upload: progress }),
      });
    } catch (error) {
      if (controller.signal.aborted) return;
      if (error instanceof PartFailedError && retryable(error.failure))
        fail(
          { kind: "part_failed", part: error.part, partCount: upload.part_count },
          upload,
          error.part,
        );
      else
        fail(
          failureOfError(error instanceof PartFailedError ? error.failure : error, "part", limits),
          upload,
        );
      return;
    }
    setPhase({ step: "checking", upload: sent });
    try {
      const checked = await completeBackupUpload(context, upload.id, controller.signal);
      if (controller.signal.aborted) return;
      refreshUploads();
      if (checked.status === "complete") setPhase({ step: "opened", upload: checked });
      else if (checked.rejection)
        fail(
          {
            kind: "rejected",
            reason: checked.rejection.reason,
            message: checked.rejection.message,
          },
          checked,
        );
      else fail({ kind: "error", detail: "The server couldn't open the file." });
    } catch (error) {
      if (controller.signal.aborted) return;
      fail(failureOfError(error, "complete", limits), sent, upload.part_count);
    }
  };

  const start = async (chosen: File) => {
    file.current = chosen;
    setChosenFile({ name: chosen.name, size: chosen.size });
    const context = captureAdminAuthority();
    authority.current = context;
    const controller = new AbortController();
    abort.current = controller;
    const limits = { sizeBytes: chosen.size, limitBytes: maxBytes };
    if (chosen.size > maxBytes) {
      fail({ kind: "too_large", ...limits });
      return;
    }
    setPhase({ step: "uploading", upload: null });
    const head = new Uint8Array(await chosen.slice(0, SNIFF_BYTES).arrayBuffer());
    if (controller.signal.aborted) return;
    const sniffed = sniffRestoreSet(head);
    if (sniffed !== "restore_set") {
      fail({ kind: "rejected", reason: sniffed, message: "" });
      return;
    }
    let upload: AdminBackupUpload;
    try {
      upload = await createBackupUpload(context, chosen.size, controller.signal);
    } catch (error) {
      if (!controller.signal.aborted) fail(failureOfError(error, "create", limits));
      return;
    }
    refreshUploads();
    if (controller.signal.aborted) {
      // Closed while the upload was being opened.
      void deleteBackupUpload(context, upload.id).catch(() => undefined);
      return;
    }
    await run(upload, 0);
  };

  /** Stops sending and deletes what arrived; the sweep removes it otherwise. */
  const cancel = () => {
    abort.current?.abort();
    const upload =
      phase.step === "uploading" || phase.step === "checking" || phase.step === "failed"
        ? phase.upload
        : null;
    const context = authority.current ?? captureProfileRequestContext();
    // A checked upload stays for the Uploaded backups list; the server is
    // still checking one in the checking step.
    const finished = upload?.status === "complete" || phase.step === "checking";
    if (upload && context && !finished) {
      void deleteBackupUpload(context, upload.id)
        .catch(() => undefined)
        .finally(refreshUploads);
    }
    close();
  };

  const choose = (files: FileList | null) => {
    const chosen = files?.[0];
    if (chosen) void start(chosen);
  };

  const onDrop = (event: DragEvent) => {
    event.preventDefault();
    setDragging(false);
    choose(event.dataTransfer.files);
  };

  let body;
  let footer;
  switch (phase.step) {
    case "choose":
      body = (
        <div
          onDragOver={(event) => {
            event.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
          className={cn(
            "flex flex-col items-center gap-3 rounded-xl border border-dashed px-6 py-10 text-center",
            dragging ? "border-foreground bg-muted/50" : "border-border",
          )}
        >
          <FileUp aria-hidden="true" className="text-muted-foreground size-6" />
          <p className="text-sm">Drop the file here</p>
          <Button variant="outline" size="sm" onClick={() => inputRef.current?.click()}>
            Choose a file
          </Button>
          <input
            ref={inputRef}
            type="file"
            accept=".tar,application/x-tar"
            className="sr-only"
            tabIndex={-1}
            aria-hidden="true"
            onChange={(event) => {
              choose(event.target.files);
              event.target.value = "";
            }}
          />
        </div>
      );
      break;
    case "uploading": {
      const total = chosenFile?.size ?? 0;
      const sent = phase.upload?.received_bytes ?? 0;
      body = (
        <div className="space-y-2" role="status">
          <div className="flex items-baseline justify-between gap-3 text-sm">
            <span className="min-w-0 truncate">{chosenFile?.name}</span>
            <span className="text-muted-foreground shrink-0 tabular-nums">
              {progressLabel(sent, total)}
            </span>
          </div>
          <Progress value={progressPercent(sent, total)} aria-label="Upload progress" />
        </div>
      );
      footer = (
        <Button variant="outline" onClick={cancel}>
          Cancel
        </Button>
      );
      break;
    }
    case "checking":
      body = (
        <p className="flex items-center gap-2 text-sm" role="status">
          <LoaderCircle aria-hidden="true" className="size-4 motion-safe:animate-spin" />
          Checking the file…
        </p>
      );
      break;
    case "opened": {
      const upload = phase.upload;
      body = <p className="text-sm">{openedSentence(upload)}</p>;
      footer = (
        <Button
          onClick={() => {
            close();
            void navigate(uploadRestorePath(upload.id));
          }}
        >
          Choose what to restore
        </Button>
      );
      break;
    }
    case "failed": {
      const copy = failureCopy(phase.failure);
      const { upload, from } = phase;
      body = (
        <div className="space-y-2" role="alert">
          <p className="text-sm font-medium">{copy.title}</p>
          <p className="text-muted-foreground text-sm">{copy.body}</p>
          {copy.command ? (
            <pre className="bg-muted overflow-x-auto rounded-md px-3 py-2 font-mono text-xs">
              <code>{copy.command}</code>
            </pre>
          ) : null}
          {phase.failure.kind === "rejected" && phase.failure.message ? (
            <p className="text-muted-foreground text-xs break-words">{phase.failure.message}</p>
          ) : null}
        </div>
      );
      footer = (
        <>
          <Button variant="outline" onClick={cancel}>
            {copy.resumable ? "Cancel" : "Close"}
          </Button>
          {copy.resumable && upload ? (
            <Button onClick={() => void run(upload, from)}>Try again</Button>
          ) : (
            <Button onClick={() => setPhase({ step: "choose" })}>Choose another file</Button>
          )}
        </>
      );
      break;
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) cancel();
        else onOpenChange(true);
      }}
    >
      <DialogContent aria-describedby={descriptionId}>
        <DialogHeader>
          <DialogTitle>Upload a backup</DialogTitle>
          <DialogDescription id={descriptionId}>
            Use a restore set file. Get one from Download on any Silo server&apos;s Backups page.
          </DialogDescription>
        </DialogHeader>
        {body}
        {footer ? <DialogFooter>{footer}</DialogFooter> : null}
      </DialogContent>
    </Dialog>
  );
}
