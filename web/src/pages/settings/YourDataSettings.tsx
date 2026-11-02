import { useRef, useState } from "react";
import { Download, LoaderCircle, Upload } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import {
  PROFILE_EXPORT_FILE_NAME,
  exportProfile,
  getProfileExportCapability,
  importProfile,
  type ProfileImportResult,
} from "@/api/v2/profileExport";
import { V2ProblemError } from "@/api/v2/request";
import { PageHeader } from "@/components/page/PageHeader";
import { ProfilePinDialog } from "@/components/profiles/ProfilePinDialog";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/hooks/useAuth";
import { formatFileSize } from "@/lib/mediaFormat";
import { DATA_TYPE_LABELS, dataCountsPhrase, titleList } from "@/lib/profileDataTypes";

/** A size limit as a person says it: "64 MB", not "64.0 MB". */
function sizeLimit(bytes: number): string {
  return formatFileSize(bytes).replace(/\.0(?= )/, "");
}

function requireContext() {
  const context = captureProfileRequestContext();
  if (!context) throw new StaleApiRequestContextError();
  return context;
}

/** Thrown before upload when the chosen file can't be an export. */
class ImportFileError extends Error {}

const NOT_AN_EXPORT =
  "This file isn't a profile export from Silo, or it's from a newer version of Silo.";

function needsPin(error: unknown): boolean {
  return (
    error instanceof V2ProblemError &&
    error.status === 403 &&
    error.problemType === "profile_verification_required"
  );
}

function failureMessage(error: unknown): string {
  if (error instanceof ImportFileError) return error.message;
  if (error instanceof V2ProblemError) {
    if (error.status === 422) return NOT_AN_EXPORT;
    return error.message;
  }
  return "The server didn't answer. Check your connection, then try again.";
}

function saveDocument(document: unknown) {
  const blob = new Blob([JSON.stringify(document)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const anchor = window.document.createElement("a");
  anchor.href = url;
  anchor.download = PROFILE_EXPORT_FILE_NAME;
  window.document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function ImportSummary({ result }: { result: ProfileImportResult }) {
  return (
    <div role="status" className="bg-background/60 space-y-2 rounded-lg border p-4 text-sm">
      <p className="font-medium">Import finished. Nothing on this profile was deleted.</p>
      {result.data.length > 0 ? (
        <ul className="space-y-1">
          {result.data.map((data) => (
            <li key={data.type}>
              {DATA_TYPE_LABELS[data.type]}:{" "}
              <span className="text-muted-foreground tabular-nums">{dataCountsPhrase(data)}</span>
              {data.notes.map((note, position) => (
                <span key={position} className="text-muted-foreground block text-xs">
                  {note}
                </span>
              ))}
            </li>
          ))}
        </ul>
      ) : null}
      {result.unmatched.length > 0 ? (
        <p className="text-muted-foreground text-[13px] break-words">
          Not in this server&apos;s library, so left out: {titleList(result.unmatched)}.
        </p>
      ) : null}
      {result.hidden.length > 0 ? (
        <p className="text-muted-foreground text-[13px] break-words">
          Titles this profile isn&apos;t allowed to see, so left out: {titleList(result.hidden)}.
        </p>
      ) : null}
    </div>
  );
}

/** Settings → Your Data: download this profile's own data, or merge a download back in. */
export default function YourDataSettings() {
  const { profile, selectProfile, verifyProfilePin } = useAuth();
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  const fileInput = useRef<HTMLInputElement>(null);
  const [result, setResult] = useState<ProfileImportResult | null>(null);
  const [importError, setImportError] = useState<string | null>(null);
  // What to run again once the profile's PIN is entered.
  const [afterPin, setAfterPin] = useState<(() => void) | null>(null);

  const capability = useQuery({
    queryKey: ["profile", "export", "capability", context?.serverOrigin, context?.profileId],
    queryFn: () => getProfileExportCapability(requireContext()),
    enabled: context !== null,
    staleTime: 60_000,
  });
  const maxBytes = capability.data?.max_import_bytes ?? 0;

  const exportData = useMutation({
    mutationFn: () => exportProfile(requireContext()),
    retry: false,
    onSuccess: saveDocument,
    onError: (error) => {
      if (needsPin(error)) setAfterPin(() => () => exportData.mutate());
      else toast.error("Couldn't export your data", { description: failureMessage(error) });
    },
  });

  const importData = useMutation({
    // target is the profile that chose the file, captured before the file is read: the
    // import must not land on a profile switched to while it reads.
    mutationFn: async ({ file, target }: { file: File; target: ProfileRequestContextSnapshot }) => {
      if (maxBytes > 0 && file.size > maxBytes) {
        throw new ImportFileError(
          `This file is ${formatFileSize(file.size)}. The largest export this server takes is ${sizeLimit(maxBytes)}.`,
        );
      }
      let document: unknown;
      try {
        document = JSON.parse(await file.text());
      } catch {
        throw new ImportFileError(NOT_AN_EXPORT);
      }
      if (typeof document !== "object" || document === null || Array.isArray(document)) {
        throw new ImportFileError(NOT_AN_EXPORT);
      }
      if (!isCapturedProfileAuthorityActive(target)) throw new StaleApiRequestContextError();
      return importProfile(target, document as Record<string, unknown>);
    },
    retry: false,
    onMutate: () => {
      setResult(null);
      setImportError(null);
    },
    onSuccess: (imported) => {
      setResult(imported);
      // An import can change settings, history, ratings, lists, and collections
      // at once, and the server publishes no event for it: refetch whatever
      // this profile has loaded.
      void queryClient.invalidateQueries();
    },
    onError: (error, { file }) => {
      if (needsPin(error))
        setAfterPin(() => () => importData.mutate({ file, target: requireContext() }));
      else setImportError(failureMessage(error));
    },
  });

  const header = (
    <PageHeader
      title="Your Data"
      subtitle="Download this profile's history, ratings, and lists, or merge a download back in."
    />
  );

  if (capability.isPending) {
    return (
      <div className="space-y-6">
        {header}
        <Skeleton className="h-32 w-full rounded-xl" />
      </div>
    );
  }
  if (capability.isError || capability.data?.state !== "available") {
    return (
      <div className="space-y-6">
        {header}
        <p className="text-muted-foreground text-sm">
          {capability.isError
            ? "Couldn't check whether this server can export profile data. Reload the page to try again."
            : "This server can't export or import profile data."}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {header}

      <SettingsGroup
        title="Export my data"
        description="A file with this profile's watch history and progress, ratings, favorites and watchlist, personal collections, dismissed titles, and preferences. It holds no password, PIN, or account details."
      >
        <Button
          variant="outline"
          onClick={() => exportData.mutate()}
          disabled={exportData.isPending}
        >
          {exportData.isPending ? (
            <LoaderCircle data-icon="inline-start" className="motion-safe:animate-spin" />
          ) : (
            <Download data-icon="inline-start" aria-hidden="true" />
          )}
          Export my data
        </Button>
      </SettingsGroup>

      <SettingsGroup
        title="Import data"
        description="Merges an export into this profile, on this server or another. Newer progress wins, missing entries are added, and preferences are replaced. Nothing is deleted, and importing the same file twice changes nothing."
      >
        <div className="space-y-3">
          <input
            ref={fileInput}
            type="file"
            accept=".json,application/json"
            className="sr-only"
            tabIndex={-1}
            aria-hidden="true"
            onChange={(event) => {
              const file = event.target.files?.[0];
              event.target.value = "";
              if (file) importData.mutate({ file, target: requireContext() });
            }}
          />
          <Button
            variant="outline"
            onClick={() => fileInput.current?.click()}
            disabled={importData.isPending}
          >
            {importData.isPending ? (
              <LoaderCircle data-icon="inline-start" className="motion-safe:animate-spin" />
            ) : (
              <Upload data-icon="inline-start" aria-hidden="true" />
            )}
            Choose an export file
          </Button>
          {maxBytes > 0 ? (
            <p className="text-muted-foreground text-xs">
              A .json file from Export my data, up to {sizeLimit(maxBytes)}.
            </p>
          ) : null}
          {importError ? (
            <p role="alert" className="text-destructive text-sm">
              Couldn&apos;t import the file. {importError}
            </p>
          ) : null}
          {result ? <ImportSummary result={result} /> : null}
        </div>
      </SettingsGroup>

      <ProfilePinDialog
        profile={afterPin ? profile : null}
        description="Enter this profile's PIN to export or import its data."
        onClose={() => setAfterPin(null)}
        onVerified={(verified, token) => {
          const retry = afterPin;
          setAfterPin(null);
          selectProfile(verified, token);
          retry?.();
        }}
        verifyPin={verifyProfilePin}
      />
    </div>
  );
}
