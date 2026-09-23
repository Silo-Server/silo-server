import { useEffect, useRef, useState } from "react";
import {
  getAdminSubtitleEditor,
  type AdminSubtitleEditIntent,
  type AdminSubtitleEditor,
} from "@/api/v2/adminSubtitleMetadata";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { useAdminUpdateDownloadedSubtitle } from "@/hooks/queries/admin/subtitles";
import { LANGUAGES, getLanguageName } from "@/player/utils/languageNames";
import { cn } from "@/lib/utils";
import { languageChipClass, providerBadgeClass, providerLabel } from "./subtitleAdminStyles";

interface AdminSubtitleEditSheetProps {
  intent: AdminSubtitleEditIntent | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export default function AdminSubtitleEditSheet({
  intent,
  open,
  onOpenChange,
}: AdminSubtitleEditSheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col overflow-y-auto sm:max-w-md">
        {intent ? (
          <AdminSubtitleEditorLoader
            key={intent.subtitle.id + intent.scope}
            intent={intent}
            onClose={() => onOpenChange(false)}
          />
        ) : (
          <>
            <SheetHeader>
              <SheetTitle>Edit subtitle</SheetTitle>
            </SheetHeader>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function AdminSubtitleEditorLoader({
  intent,
  onClose,
}: {
  intent: AdminSubtitleEditIntent;
  onClose: () => void;
}) {
  const [editor, setEditor] = useState<AdminSubtitleEditor | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    void getAdminSubtitleEditor(intent, controller.signal).then(
      (value) => {
        if (active) setEditor(value);
      },
      (error: unknown) => {
        if (active)
          setError(error instanceof Error ? error.message : "Unable to load subtitle metadata.");
      },
    );
    return () => {
      active = false;
      controller.abort();
    };
  }, [intent]);
  if (!editor)
    return (
      <>
        <SheetHeader>
          <SheetTitle>Edit subtitle</SheetTitle>
          <SheetDescription>Load current metadata before making changes.</SheetDescription>
        </SheetHeader>
        <p role={error ? "alert" : "status"}>{error ?? "Loading subtitle metadata…"}</p>
      </>
    );
  return <AdminSubtitleEditForm editor={editor} onClose={onClose} />;
}

function AdminSubtitleEditForm({
  editor,
  onClose,
}: {
  editor: AdminSubtitleEditor;
  onClose: () => void;
}) {
  const subtitle = editor.body;
  const updateMutation = useAdminUpdateDownloadedSubtitle();
  const active = useRef(true);
  const saving = useRef(false);
  const [failure, setFailure] = useState<string | null>(null);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  const [language, setLanguage] = useState(subtitle.language);
  const [releaseName, setReleaseName] = useState(subtitle.release_name);
  const [hearingImpaired, setHearingImpaired] = useState(subtitle.hearing_impaired);

  const languageChanged = language !== subtitle.language;

  async function handleSave() {
    if (saving.current || failure) return;
    saving.current = true;
    try {
      await updateMutation.mutateAsync({
        editor,
        patch: {
          ...(language !== subtitle.language ? { language } : {}),
          ...(releaseName !== subtitle.release_name ? { release_name: releaseName } : {}),
          ...(hearingImpaired !== subtitle.hearing_impaired
            ? { hearing_impaired: hearingImpaired }
            : {}),
        },
      });
      if (active.current) onClose();
    } catch (error) {
      if (active.current)
        setFailure(error instanceof Error ? error.message : "Save was not confirmed.");
    } finally {
      saving.current = false;
    }
  }

  return (
    <>
      <SheetHeader>
        <SheetTitle>Edit subtitle</SheetTitle>
        <SheetDescription>
          Update stored metadata for this subtitle record. File content is not replaced.
        </SheetDescription>
      </SheetHeader>

      <div className="space-y-6 px-1 py-2">
        <div className="surface-panel-subtle space-y-3 rounded-xl px-4 py-4">
          <div className="text-sm font-semibold">
            {editor.intent.subtitle.media_title || "Unknown media"}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span
              className={cn(
                "inline-flex rounded-full border px-2.5 py-1 text-xs font-medium",
                languageChipClass(),
              )}
            >
              {subtitle.language.toUpperCase()} · {getLanguageName(subtitle.language)}
            </span>
            <span
              className={cn(
                "inline-flex rounded-full border px-2.5 py-1 text-xs font-medium",
                providerBadgeClass(subtitle.provider),
              )}
            >
              {providerLabel(subtitle.provider)}
            </span>
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="subtitle-language">Language</Label>
          <Select value={language} onValueChange={setLanguage}>
            <SelectTrigger id="subtitle-language">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LANGUAGES.map((lang) => (
                <SelectItem key={lang.code} value={lang.code}>
                  {lang.code.toUpperCase()} · {lang.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {languageChanged && (
            <p className="text-muted-foreground text-xs leading-relaxed">
              Updates stored language label; file content unchanged.
            </p>
          )}
        </div>

        <div className="space-y-2">
          <Label htmlFor="subtitle-release-name">Release name</Label>
          <Input
            id="subtitle-release-name"
            value={releaseName}
            onChange={(event) => setReleaseName(event.target.value)}
            className="font-mono text-xs"
          />
        </div>

        <div className="border-border/60 flex items-center justify-between rounded-xl border px-4 py-3">
          <div className="space-y-1">
            <Label htmlFor="subtitle-hearing-impaired">Hearing impaired</Label>
            <p className="text-muted-foreground text-xs">Marks this track as SDH/CC.</p>
          </div>
          <Switch
            id="subtitle-hearing-impaired"
            checked={hearingImpaired}
            onCheckedChange={setHearingImpaired}
          />
        </div>
      </div>

      {failure && (
        <p role="alert">
          {failure} Your edits remain here. Close and reopen to review current metadata before
          another save.
        </p>
      )}

      <SheetFooter className="mt-auto gap-2 sm:justify-end">
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button
          type="button"
          disabled={
            updateMutation.isPending ||
            !!failure ||
            (language === subtitle.language &&
              releaseName === subtitle.release_name &&
              hearingImpaired === subtitle.hearing_impaired)
          }
          onClick={() => void handleSave()}
        >
          Save changes
        </Button>
      </SheetFooter>
    </>
  );
}
