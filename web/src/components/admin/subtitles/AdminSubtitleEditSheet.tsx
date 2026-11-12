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
import { useT } from "@/i18n";
import { languageOptions, getLanguageName } from "@/player/utils/languageNames";
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
  const t = useT();
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
              <SheetTitle>{t("web.admin.common.subtitles.edit.title")}</SheetTitle>
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
  const t = useT();
  const [editor, setEditor] = useState<AdminSubtitleEditor | null>(null);
  // The load failure; its text is read at render, in the interface language.
  const [error, setError] = useState<{ cause: unknown } | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    void getAdminSubtitleEditor(intent, controller.signal).then(
      (value) => {
        if (active) setEditor(value);
      },
      (error: unknown) => {
        if (active) setError({ cause: error });
      },
    );
    return () => {
      active = false;
      controller.abort();
    };
  }, [intent]);
  const errorText =
    error &&
    (error.cause instanceof Error
      ? error.cause.message
      : t("web.admin.common.subtitles.edit.loadFailed"));
  if (!editor)
    return (
      <>
        <SheetHeader>
          <SheetTitle>{t("web.admin.common.subtitles.edit.title")}</SheetTitle>
          <SheetDescription>{t("web.admin.common.subtitles.edit.loadHint")}</SheetDescription>
        </SheetHeader>
        <p role={errorText ? "alert" : "status"}>
          {errorText ?? t("web.admin.common.subtitles.edit.loading")}
        </p>
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
  const t = useT();
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
        setFailure(
          error instanceof Error ? error.message : t("web.admin.common.subtitles.edit.saveFailed"),
        );
    } finally {
      saving.current = false;
    }
  }

  return (
    <>
      <SheetHeader>
        <SheetTitle>{t("web.admin.common.subtitles.edit.title")}</SheetTitle>
        <SheetDescription>{t("web.admin.common.subtitles.edit.description")}</SheetDescription>
      </SheetHeader>

      <div className="space-y-6 px-1 py-2">
        <div className="surface-panel-subtle space-y-3 rounded-xl px-4 py-4">
          <div className="text-sm font-semibold">
            {editor.intent.subtitle.media_title || t("web.admin.common.subtitles.unknownMedia")}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span
              className={cn(
                "inline-flex rounded-full border px-2.5 py-1 text-xs font-medium",
                languageChipClass(),
              )}
            >
              {t("web.admin.common.subtitles.languageCode", {
                code: subtitle.language.toUpperCase(),
                language: getLanguageName(subtitle.language),
              })}
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
          <Label htmlFor="subtitle-language">{t("web.admin.common.subtitles.edit.language")}</Label>
          <Select value={language} onValueChange={setLanguage}>
            <SelectTrigger id="subtitle-language">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {languageOptions().map((lang) => (
                <SelectItem key={lang.code} value={lang.code}>
                  {t("web.admin.common.subtitles.languageCode", {
                    code: lang.code.toUpperCase(),
                    language: lang.label,
                  })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {languageChanged && (
            <p className="text-muted-foreground text-xs leading-relaxed">
              {t("web.admin.common.subtitles.edit.languageChanged")}
            </p>
          )}
        </div>

        <div className="space-y-2">
          <Label htmlFor="subtitle-release-name">
            {t("web.admin.common.subtitles.edit.releaseName")}
          </Label>
          <Input
            id="subtitle-release-name"
            value={releaseName}
            onChange={(event) => setReleaseName(event.target.value)}
            className="font-mono text-xs"
          />
        </div>

        <div className="border-border/60 flex items-center justify-between rounded-xl border px-4 py-3">
          <div className="space-y-1">
            <Label htmlFor="subtitle-hearing-impaired">
              {t("web.admin.common.subtitles.edit.hearingImpaired")}
            </Label>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.common.subtitles.edit.hearingImpairedHelp")}
            </p>
          </div>
          <Switch
            id="subtitle-hearing-impaired"
            checked={hearingImpaired}
            onCheckedChange={setHearingImpaired}
          />
        </div>
      </div>

      {failure && (
        <p role="alert">{t("web.admin.common.subtitles.edit.failureHint", { error: failure })}</p>
      )}

      <SheetFooter className="mt-auto gap-2 sm:justify-end">
        <Button type="button" variant="outline" onClick={onClose}>
          {t("common.cancel")}
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
          {t("web.admin.common.subtitles.edit.saveChanges")}
        </Button>
      </SheetFooter>
    </>
  );
}
