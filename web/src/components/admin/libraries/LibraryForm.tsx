import type { FormEvent, ReactNode } from "react";

import type { Library } from "@/api/types";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

import { AdvancedFields, FolderFields, GeneralFields, MetadataFields } from "./LibraryFormSections";
import { useLibraryForm } from "./useLibraryForm";

export interface LibraryFormProps {
  library: Library | null;
  chapterThumbnailsSupported: boolean;
  onClose?: () => void;
  onSaved?: (library: Library) => void;
  resetAfterCreate?: boolean;
  submitLabel?: string;
  savingLabel?: string;
}

function FormSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-3">
      <h3 className="text-muted-foreground text-xs font-semibold tracking-[0.1em] uppercase">
        {title}
      </h3>
      {children}
    </section>
  );
}

/**
 * Inline (non-dialog) library form used by the setup wizard. The admin
 * Libraries page uses LibraryEditorDialog, which renders the same sections
 * behind section tabs (a side rail from sm up, a top strip on a phone).
 */
export function LibraryForm({
  library,
  chapterThumbnailsSupported,
  onClose,
  onSaved,
  resetAfterCreate = false,
  submitLabel,
  savingLabel,
}: LibraryFormProps) {
  const t = useT();
  const form = useLibraryForm({ library, onClose, onSaved, resetAfterCreate });

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    form.submit();
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-6">
      <FormSection title={t("web.admin.common.libraryEditor.section.general")}>
        <GeneralFields form={form} />
      </FormSection>
      <FormSection title={t("web.admin.common.libraryEditor.section.folders")}>
        <FolderFields form={form} />
      </FormSection>
      <FormSection title={t("web.admin.common.libraryEditor.section.metadata")}>
        <MetadataFields form={form} />
      </FormSection>
      {(form.settingSupport.chapterThumbnails || form.settingSupport.introDetection) && (
        <FormSection title={t("web.admin.common.libraryEditor.section.advanced")}>
          <AdvancedFields form={form} chapterThumbnailsSupported={chapterThumbnailsSupported} />
        </FormSection>
      )}
      <Button type="submit" className="w-full" disabled={form.isPending}>
        {form.isPending
          ? (savingLabel ?? t("web.admin.common.libraryEditor.form.saving"))
          : (submitLabel ?? t("common.save"))}
      </Button>
    </form>
  );
}
