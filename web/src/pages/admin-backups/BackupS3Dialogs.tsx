import { useId, useState } from "react";
import { LoaderCircle, Lock } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { V2ProblemError } from "@/api/v2/request";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useAcknowledgeAdminBackupKey, useTestAdminBackupS3 } from "@/hooks/queries/admin/backups";
import { useUpdateServerSettings } from "@/hooks/queries/admin/settings";
import { adminKeys } from "@/hooks/queries/keys";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { backupErrorMessage } from "./backupPresentation";
import {
  currentS3Test,
  s3SaveBlocker,
  s3SettingsValues,
  s3TestBody,
  type S3Fields,
  type S3TestOutcome,
} from "./s3SetupModel";

/** What to say when confirming the key failed. */
function acknowledgeErrorMessage(error: unknown): string {
  if (error instanceof V2ProblemError && error.status === 409)
    return "This server's SECRET_KEY changed since the page loaded. Check the new key check, then confirm again.";
  return backupErrorMessage(error);
}

/** The SECRET_KEY warning, the key check, and the "I keep a copy" box. */
function KeyBox({
  keyCheck,
  kept,
  onKeptChange,
}: {
  keyCheck: string;
  kept: boolean;
  onKeptChange: (kept: boolean) => void;
}) {
  const headingId = useId();
  const checkLabelId = useId();
  const boxId = useId();
  return (
    <section aria-labelledby={headingId} className="bg-muted/40 space-y-3 rounded-xl border p-4">
      <h3 id={headingId} className="flex items-center gap-2 text-sm font-semibold">
        <Lock aria-hidden="true" className="size-4 shrink-0" />
        Backups here are encrypted with SECRET_KEY
      </h3>
      <p className="text-muted-foreground text-[13px] leading-relaxed">
        Restoring one needs this server&apos;s SECRET_KEY. Silo never shows the key and doesn&apos;t
        keep a copy of it anywhere else. If the key exists only in this server&apos;s .env file and
        the disk fails, these backups can&apos;t be restored, and no one can recover them.
      </p>
      <div className="space-y-1">
        <p id={checkLabelId} className="text-xs font-medium">
          Key check
        </p>
        <p
          aria-labelledby={checkLabelId}
          className="bg-background inline-block rounded-md border px-3 py-1.5 font-mono text-sm tracking-wide"
        >
          {keyCheck}
        </p>
      </div>
      <p className="text-muted-foreground text-[13px] leading-relaxed">
        To check the copy you saved, run{" "}
        <code className="font-mono text-xs">silo secret check</code> with it on any computer. The
        same key gives the same code.
      </p>
      <div className="flex items-start gap-2.5">
        <Checkbox
          id={boxId}
          checked={kept}
          onCheckedChange={(value) => onKeptChange(value === true)}
          aria-required="true"
          className="mt-0.5"
        />
        <Label htmlFor={boxId} className="text-[13px] leading-relaxed font-normal">
          I keep a copy of SECRET_KEY somewhere other than this server. Without it, these backups
          can&apos;t be restored.
        </Label>
      </div>
    </section>
  );
}

function TextField({
  label,
  value,
  onChange,
  placeholder,
  type = "text",
  mono = false,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  type?: "text" | "password";
  mono?: boolean;
}) {
  const id = useId();
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type={type}
        value={value}
        placeholder={placeholder}
        onChange={(event) => onChange(event.target.value)}
        autoComplete="off"
        spellCheck={false}
        className={mono ? "font-mono text-xs" : undefined}
      />
    </div>
  );
}

function S3SetupForm({
  initial,
  secretConfigured,
  keyCheck,
  onSaved,
  onDone,
}: {
  initial: S3Fields;
  secretConfigured: boolean;
  keyCheck: string;
  onSaved: () => void;
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [fields, setFields] = useState(initial);
  const [outcome, setOutcome] = useState<S3TestOutcome | null>(null);
  const [kept, setKept] = useState(false);
  const [saving, setSaving] = useState(false);
  const pathStyleId = useId();
  const blockerId = useId();
  const testS3 = useTestAdminBackupS3();
  const acknowledge = useAcknowledgeAdminBackupKey();
  const updateSettings = useUpdateServerSettings();

  const set =
    <K extends keyof S3Fields>(key: K) =>
    (value: S3Fields[K]) =>
      setFields((current) => ({ ...current, [key]: value }));
  const test = currentS3Test(fields, outcome);
  const blocker = s3SaveBlocker({ fields, outcome, keyKept: kept });

  const runTest = () => {
    const body = s3TestBody(fields);
    testS3.mutate(body, {
      onSuccess: (result) => setOutcome({ body, result }),
      onError: (error) =>
        setOutcome({ body, result: { ok: false, message: backupErrorMessage(error) } }),
    });
  };

  const save = async () => {
    setSaving(true);
    try {
      try {
        await acknowledge.mutateAsync(keyCheck);
      } catch (error) {
        toast.error("Couldn't confirm the key", { description: acknowledgeErrorMessage(error) });
        return;
      }
      try {
        await updateSettings.mutateAsync(s3SettingsValues(fields));
      } catch {
        // The settings mutation reports its own failure.
        return;
      }
      await queryClient.invalidateQueries({ queryKey: adminKeys.backups() });
      onSaved();
      toast.success("Backups now go to the bucket", {
        description: "The next backup is the first one there.",
      });
      onDone();
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <DialogHeader>
        <DialogTitle>Keep backups in an S3 bucket</DialogTitle>
        <DialogDescription>
          Use a bucket just for backups, with keys that can only reach it.
        </DialogDescription>
      </DialogHeader>
      {/* The fields scroll between a fixed header and footer, so Save stays
          on screen however short the window is. */}
      <div className="-mx-6 min-h-0 space-y-5 overflow-y-auto px-6 py-1">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="sm:col-span-2">
            <TextField
              label="Endpoint"
              value={fields.endpoint}
              onChange={set("endpoint")}
              placeholder="https://s3.us-east-1.amazonaws.com"
              mono
            />
          </div>
          <TextField label="Region" value={fields.region} onChange={set("region")} />
          <TextField label="Bucket" value={fields.bucket} onChange={set("bucket")} mono />
          <div className="sm:col-span-2">
            <TextField
              label="Folder in the bucket"
              value={fields.keyPrefix}
              onChange={set("keyPrefix")}
              mono
            />
          </div>
          <TextField label="Access key" value={fields.accessKey} onChange={set("accessKey")} mono />
          <TextField
            label="Secret key"
            type="password"
            value={fields.secretKey}
            onChange={set("secretKey")}
            placeholder={secretConfigured ? "Set" : undefined}
          />
          <div className="flex items-center gap-2.5 sm:col-span-2">
            <Switch
              id={pathStyleId}
              checked={fields.pathStyle}
              onCheckedChange={set("pathStyle")}
            />
            <Label htmlFor={pathStyleId}>Path-style addressing</Label>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="outline"
            onClick={runTest}
            disabled={testS3.isPending || !fields.endpoint.trim() || !fields.bucket.trim()}
          >
            {testS3.isPending ? (
              <LoaderCircle data-icon="inline-start" className="motion-safe:animate-spin" />
            ) : null}
            Test connection
          </Button>
          {test && !testS3.isPending ? (
            <div role="status">
              <SettingFieldStatus tone={test.ok ? "ok" : "warn"}>{test.message}</SettingFieldStatus>
            </div>
          ) : null}
        </div>
        <KeyBox keyCheck={keyCheck} kept={kept} onKeptChange={setKept} />
      </div>
      <DialogFooter className="items-center">
        {blocker ? (
          <p id={blockerId} className="text-muted-foreground mr-auto text-xs">
            {blocker}
          </p>
        ) : null}
        <Button variant="ghost" onClick={onDone}>
          Cancel
        </Button>
        <Button
          onClick={() => void save()}
          disabled={blocker !== null || saving}
          aria-describedby={blocker ? blockerId : undefined}
        >
          {saving ? (
            <LoaderCircle data-icon="inline-start" className="motion-safe:animate-spin" />
          ) : null}
          Save and use this bucket
        </Button>
      </DialogFooter>
    </>
  );
}

/** Sets up the S3 destination: test the bucket, confirm the key, save. */
export function S3SetupDialog({
  open,
  onOpenChange,
  initial,
  secretConfigured,
  keyCheck,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called once the bucket is saved as the destination. */
  onSaved: () => void;
  /** The saved bucket settings, read when the dialog opens. */
  initial: S3Fields;
  /** A secret key is stored, so leaving the field empty keeps it. */
  secretConfigured: boolean;
  /** This server's SECRET_KEY check value, from the backup list. */
  keyCheck: string;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden sm:max-w-xl">
        {/* Content unmounts on close, so each opening starts from the saved values. */}
        <S3SetupForm
          initial={initial}
          secretConfigured={secretConfigured}
          keyCheck={keyCheck}
          onSaved={onSaved}
          onDone={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

function ConfirmKeyForm({ keyCheck, onDone }: { keyCheck: string; onDone: () => void }) {
  const [kept, setKept] = useState(false);
  const acknowledge = useAcknowledgeAdminBackupKey();
  const confirm = () =>
    acknowledge.mutate(keyCheck, {
      onSuccess: () => {
        toast.success("Key confirmed", { description: "Backups to the bucket run again." });
        onDone();
      },
      onError: (error) =>
        toast.error("Couldn't confirm the key", { description: acknowledgeErrorMessage(error) }),
    });
  return (
    <>
      <DialogHeader>
        <DialogTitle>Confirm the SECRET_KEY</DialogTitle>
        <DialogDescription>
          Backups to the bucket are paused until you confirm you keep a copy of this server&apos;s
          SECRET_KEY.
        </DialogDescription>
      </DialogHeader>
      <KeyBox keyCheck={keyCheck} kept={kept} onKeptChange={setKept} />
      <DialogFooter>
        <Button variant="ghost" onClick={onDone}>
          Cancel
        </Button>
        <Button onClick={confirm} disabled={!kept || acknowledge.isPending}>
          {acknowledge.isPending ? (
            <LoaderCircle data-icon="inline-start" className="motion-safe:animate-spin" />
          ) : null}
          Confirm the key
        </Button>
      </DialogFooter>
    </>
  );
}

/**
 * Confirms a changed (or never confirmed) SECRET_KEY so encrypted backups
 * resume: the S3 destination, or this server's storage when it is a bucket.
 */
export function ConfirmKeyDialog({
  open,
  onOpenChange,
  keyCheck,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  keyCheck: string;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <ConfirmKeyForm keyCheck={keyCheck} onDone={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  );
}
