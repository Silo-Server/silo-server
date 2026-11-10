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
import { useT, type Translate } from "@/i18n";
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

function needsPin(error: unknown): boolean {
  return (
    error instanceof V2ProblemError &&
    error.status === 403 &&
    error.problemType === "profile_verification_required"
  );
}

function failureMessage(t: Translate, error: unknown): string {
  if (error instanceof ImportFileError) return error.message;
  if (error instanceof V2ProblemError) {
    if (error.status === 422) return t("web.settings.yourData.import.notAnExport");
    return error.message;
  }
  return t("web.settings.yourData.noAnswer");
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
  const t = useT();
  return (
    <div role="status" className="bg-background/60 space-y-2 rounded-lg border p-4 text-sm">
      <p className="font-medium">{t("web.settings.yourData.summary.finished")}</p>
      {result.data.length > 0 ? (
        <ul className="space-y-1">
          {result.data.map((data) => (
            <li key={data.type}>
              {t("web.settings.yourData.summary.line", {
                type: DATA_TYPE_LABELS[data.type],
                counts: dataCountsPhrase(data),
                muted: (chunks) => (
                  <span className="text-muted-foreground tabular-nums">{chunks}</span>
                ),
              })}
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
          {t("web.settings.yourData.summary.unmatched", { titles: titleList(result.unmatched) })}
        </p>
      ) : null}
      {result.hidden.length > 0 ? (
        <p className="text-muted-foreground text-[13px] break-words">
          {t("web.settings.yourData.summary.hidden", { titles: titleList(result.hidden) })}
        </p>
      ) : null}
    </div>
  );
}

/** Settings → Your Data: download this profile's own data, or merge a download back in. */
export default function YourDataSettings() {
  const t = useT();
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
      else
        toast.error(t("web.settings.yourData.export.failed"), {
          description: failureMessage(t, error),
        });
    },
  });

  const importData = useMutation({
    // target is the profile that chose the file, captured before the file is read: the
    // import must not land on a profile switched to while it reads.
    mutationFn: async ({ file, target }: { file: File; target: ProfileRequestContextSnapshot }) => {
      if (maxBytes > 0 && file.size > maxBytes) {
        throw new ImportFileError(
          t("web.settings.yourData.import.tooLarge", {
            size: formatFileSize(file.size),
            limit: sizeLimit(maxBytes),
          }),
        );
      }
      let document: unknown;
      try {
        document = JSON.parse(await file.text());
      } catch {
        throw new ImportFileError(t("web.settings.yourData.import.notAnExport"));
      }
      if (typeof document !== "object" || document === null || Array.isArray(document)) {
        throw new ImportFileError(t("web.settings.yourData.import.notAnExport"));
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
      else setImportError(failureMessage(t, error));
    },
  });

  const header = (
    <PageHeader
      title={t("web.settings.yourData.title")}
      subtitle={t("web.settings.yourData.subtitle")}
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
            ? t("web.settings.yourData.capabilityFailed")
            : t("web.settings.yourData.unavailable")}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {header}

      <SettingsGroup
        title={t("web.settings.yourData.export.title")}
        description={t("web.settings.yourData.export.description")}
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
          {t("web.settings.yourData.export.button")}
        </Button>
      </SettingsGroup>

      <SettingsGroup
        title={t("web.settings.yourData.import.title")}
        description={t("web.settings.yourData.import.description")}
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
            {t("web.settings.yourData.import.chooseFile")}
          </Button>
          {maxBytes > 0 ? (
            <p className="text-muted-foreground text-xs">
              {t("web.settings.yourData.import.fileHint", { limit: sizeLimit(maxBytes) })}
            </p>
          ) : null}
          {importError ? (
            <p role="alert" className="text-destructive text-sm">
              {t("web.settings.yourData.import.failed", { reason: importError })}
            </p>
          ) : null}
          {result ? <ImportSummary result={result} /> : null}
        </div>
      </SettingsGroup>

      <ProfilePinDialog
        profile={afterPin ? profile : null}
        description={t("web.settings.yourData.pinDescription")}
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
