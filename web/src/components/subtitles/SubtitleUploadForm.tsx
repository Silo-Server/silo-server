import { useCallback, useRef, useState } from "react";
import { Loader2, Upload } from "lucide-react";

import type { SubtitleLanguageDetection } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import { LANGUAGES, getLanguageName } from "@/player/utils/languageNames";

const ACCEPTED_SUBTITLE_EXTENSIONS = ".srt,.vtt,.ass,.ssa,.sub";
const ACCEPTED_SUBTITLE_EXTENSION_LIST = ["srt", "vtt", "ass", "ssa", "sub"] as const;

export interface SubtitleUploadInput {
  mediaFileId: number;
  file: File;
  language?: string;
  languageOverride?: boolean;
  hearingImpaired: boolean;
}

interface SubtitleUploadFormProps {
  mediaFileId: number;
  upload: (input: SubtitleUploadInput) => Promise<void>;
  detectLanguage?: (file: File, fallbackLanguage?: string) => Promise<SubtitleLanguageDetection>;
  onSuccess: () => void;
  onError?: (message: string) => void;
  variant?: "player" | "default";
  defaultLanguage?: string;
}

function isAcceptedSubtitleFile(file: File): boolean {
  const extension = file.name.split(".").pop()?.toLowerCase() ?? "";
  return ACCEPTED_SUBTITLE_EXTENSION_LIST.includes(
    extension as (typeof ACCEPTED_SUBTITLE_EXTENSION_LIST)[number],
  );
}

function detectionSourceLabel(source: SubtitleLanguageDetection["source"]): string {
  switch (source) {
    case "filename":
      return "filename";
    case "metadata":
      return "file metadata";
    case "content":
      return "subtitle text";
    case "manual":
      return "manual selection";
    default:
      return "detection";
  }
}

export function SubtitleUploadForm({
  mediaFileId,
  upload,
  detectLanguage,
  onSuccess,
  onError,
  variant = "default",
  defaultLanguage = "en",
}: SubtitleUploadFormProps) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const dragDepthRef = useRef(0);
  const detectRequestRef = useRef(0);
  const [language, setLanguage] = useState(defaultLanguage);
  const [hearingImpaired, setHearingImpaired] = useState(false);
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);
  const [detectingLanguage, setDetectingLanguage] = useState(false);
  const [isDragging, setIsDragging] = useState(false);
  const [detectionSource, setDetectionSource] = useState<
    SubtitleLanguageDetection["source"] | null
  >(null);
  const [languageOverride, setLanguageOverride] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isPlayer = variant === "player";

  const reportError = useCallback(
    (message: string) => {
      setError(message);
      onError?.(message);
    },
    [onError],
  );

  const runLanguageDetection = useCallback(
    async (file: File, fallbackLanguage: string) => {
      if (!detectLanguage) {
        return;
      }

      const requestId = ++detectRequestRef.current;
      setDetectingLanguage(true);

      try {
        const result = await detectLanguage(file, fallbackLanguage);
        if (requestId !== detectRequestRef.current) {
          return;
        }
        if (result.language) {
          setLanguage(result.language);
          setDetectionSource(result.source);
          setLanguageOverride(false);
        }
      } catch (err) {
        if (requestId !== detectRequestRef.current) {
          return;
        }
        setDetectionSource(null);
        reportError(err instanceof Error ? err.message : "Failed to detect subtitle language");
      } finally {
        if (requestId === detectRequestRef.current) {
          setDetectingLanguage(false);
        }
      }
    },
    [detectLanguage, reportError],
  );

  const selectFile = useCallback(
    (file: File | null | undefined) => {
      if (!file) {
        return;
      }
      if (!isAcceptedSubtitleFile(file)) {
        reportError("Unsupported file type. Use SRT, VTT, ASS, SSA, or SUB.");
        return;
      }
      setSelectedFile(file);
      setError(null);
      void runLanguageDetection(file, language);
    },
    [language, reportError, runLanguageDetection],
  );

  const handleFileChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    selectFile(event.target.files?.[0]);
  };

  const handleBrowseClick = () => {
    fileInputRef.current?.click();
  };

  const handleDragEnter = (event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    event.stopPropagation();
    dragDepthRef.current += 1;
    setIsDragging(true);
  };

  const handleDragOver = (event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    event.stopPropagation();
    event.dataTransfer.dropEffect = "copy";
  };

  const handleDragLeave = (event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    event.stopPropagation();
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
    if (dragDepthRef.current === 0) {
      setIsDragging(false);
    }
  };

  const handleDrop = (event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    event.stopPropagation();
    dragDepthRef.current = 0;
    setIsDragging(false);

    const file = event.dataTransfer.files[0];
    selectFile(file);
  };

  const handleLanguageChange = (value: string) => {
    setLanguage(value);
    setDetectionSource("manual");
    setLanguageOverride(true);
  };

  const handleUpload = async () => {
    if (!selectedFile) {
      reportError("Choose a subtitle file to upload");
      return;
    }

    setUploading(true);
    setError(null);

    try {
      await upload({
        mediaFileId,
        file: selectedFile,
        language,
        languageOverride,
        hearingImpaired,
      });
      setSelectedFile(null);
      setDetectionSource(null);
      setLanguageOverride(false);
      if (fileInputRef.current) {
        fileInputRef.current.value = "";
      }
      onSuccess();
    } catch (err) {
      reportError(err instanceof Error ? err.message : "Upload failed");
    } finally {
      setUploading(false);
    }
  };

  return (
    <div
      className={cn(
        "space-y-3",
        isPlayer ? "border-b border-white/10 px-4 py-3" : "rounded-xl border border-dashed p-4",
      )}
    >
      <div className="space-y-1">
        <p className={cn("text-sm font-medium", isPlayer ? "text-white" : "text-foreground")}>
          Upload subtitle
        </p>
        <p className={cn("text-xs", isPlayer ? "text-white/50" : "text-muted-foreground")}>
          Drag and drop or browse for SRT, VTT, ASS, SSA, or SUB files up to 5 MB. Language is
          detected automatically when possible.
        </p>
      </div>

      <input
        ref={fileInputRef}
        type="file"
        accept={ACCEPTED_SUBTITLE_EXTENSIONS}
        className="sr-only"
        onChange={handleFileChange}
      />

      <div
        role="button"
        tabIndex={0}
        aria-label="Drop subtitle file here or browse"
        onClick={handleBrowseClick}
        onKeyDown={(event) => {
          if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            handleBrowseClick();
          }
        }}
        onDragEnter={handleDragEnter}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
        className={cn(
          "flex cursor-pointer flex-col items-center justify-center gap-2 rounded-lg border border-dashed px-4 py-6 text-center transition-colors",
          isPlayer
            ? isDragging
              ? "border-white/50 bg-white/10"
              : "border-white/20 bg-white/5 hover:border-white/35 hover:bg-white/10"
            : isDragging
              ? "border-primary bg-primary/5"
              : "border-border/70 bg-muted/20 hover:border-border hover:bg-muted/40",
        )}
      >
        <Upload
          className={cn("size-5", isPlayer ? "text-white/70" : "text-muted-foreground")}
          aria-hidden="true"
        />
        <div className="space-y-1">
          <p className={cn("text-sm font-medium", isPlayer ? "text-white" : "text-foreground")}>
            {isDragging ? "Drop subtitle file" : "Drag and drop a subtitle file"}
          </p>
          <p className={cn("text-xs", isPlayer ? "text-white/50" : "text-muted-foreground")}>
            or click to browse
          </p>
        </div>
      </div>

      <div className={cn("flex flex-col gap-2", !isPlayer && "sm:flex-row sm:items-center")}>
        <div className={cn("space-y-1", !isPlayer && "w-full sm:w-[220px]")}>
          {isPlayer ? (
            <select
              aria-label="Upload language"
              className="w-full rounded bg-neutral-800 px-2 py-1.5 text-sm text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
              value={language}
              onChange={(event) => handleLanguageChange(event.target.value)}
              disabled={detectingLanguage}
            >
              {LANGUAGES.map((lang) => (
                <option key={lang.code} value={lang.code}>
                  {lang.label}
                </option>
              ))}
            </select>
          ) : (
            <Select
              value={language}
              onValueChange={handleLanguageChange}
              disabled={detectingLanguage}
            >
              <SelectTrigger>
                <SelectValue placeholder="Language" />
              </SelectTrigger>
              <SelectContent>
                {LANGUAGES.map((lang) => (
                  <SelectItem key={lang.code} value={lang.code}>
                    {lang.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          {detectingLanguage ? (
            <p className={cn("text-xs", isPlayer ? "text-white/50" : "text-muted-foreground")}>
              Detecting language…
            </p>
          ) : detectionSource && detectionSource !== "manual" ? (
            <p className={cn("text-xs", isPlayer ? "text-white/50" : "text-muted-foreground")}>
              Detected {getLanguageName(language)} from {detectionSourceLabel(detectionSource)}
            </p>
          ) : null}
        </div>

        {isPlayer ? (
          <label className="flex items-center gap-2 text-xs text-white/70">
            <input
              type="checkbox"
              checked={hearingImpaired}
              onChange={(event) => setHearingImpaired(event.target.checked)}
              className="rounded border-white/20 bg-neutral-800"
            />
            Hearing impaired (HI)
          </label>
        ) : (
          <div className="flex items-center gap-2">
            <Switch
              id={`subtitle-upload-hi-${mediaFileId}`}
              checked={hearingImpaired}
              onCheckedChange={setHearingImpaired}
            />
            <Label htmlFor={`subtitle-upload-hi-${mediaFileId}`} className="text-sm font-normal">
              Hearing impaired (HI)
            </Label>
          </div>
        )}

        {isPlayer ? (
          <button
            type="button"
            className="inline-flex items-center justify-center gap-2 rounded bg-white/10 px-3 py-1.5 text-sm font-medium hover:bg-white/20 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:opacity-50"
            onClick={handleUpload}
            disabled={uploading || detectingLanguage || !selectedFile}
          >
            {uploading ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Upload className="size-4" />
            )}
            {uploading ? "Uploading…" : "Upload"}
          </button>
        ) : (
          <Button
            onClick={handleUpload}
            disabled={uploading || detectingLanguage || !selectedFile}
            className="sm:ml-auto"
          >
            {uploading ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Upload className="size-4" />
            )}
            {uploading ? "Uploading…" : "Upload"}
          </Button>
        )}
      </div>

      {selectedFile && (
        <p className={cn("truncate text-xs", isPlayer ? "text-white/60" : "text-muted-foreground")}>
          Selected: {selectedFile.name}
        </p>
      )}

      {error && (
        <div
          role="alert"
          className={cn(
            "rounded px-3 py-2 text-xs",
            isPlayer
              ? "bg-red-900/40 text-red-300"
              : "bg-rose-500/10 text-rose-700 dark:text-rose-300",
          )}
        >
          {error}
        </div>
      )}
    </div>
  );
}
