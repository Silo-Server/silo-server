import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Database, FolderOpen, Settings2, SlidersHorizontal, XIcon } from "lucide-react";

import type { Library } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { useT, type MessageKey } from "@/i18n";
import { cn } from "@/lib/utils";

import {
  AdvancedFields,
  FolderFields,
  GeneralFields,
  MetadataFields,
  RealtimeMonitoringFields,
} from "./LibraryFormSections";
import { libraryTypeMeta } from "./libraryTypes";
import { LibraryPosterSection } from "./LibraryPosterSection";
import { useLibraryForm } from "./useLibraryForm";
import type { LibraryFormErrors } from "./useLibraryForm";

type SectionId = "general" | "folders" | "metadata" | "advanced";

const SECTIONS: Array<{
  id: SectionId;
  icon: typeof Settings2;
  title: MessageKey;
  description: MessageKey;
}> = [
  {
    id: "general",
    icon: Settings2,
    title: "web.admin.common.libraryEditor.section.general",
    description: "web.admin.common.libraryEditor.section.generalDescription",
  },
  {
    id: "folders",
    icon: FolderOpen,
    title: "web.admin.common.libraryEditor.section.folders",
    description: "web.admin.common.libraryEditor.section.foldersDescription",
  },
  {
    id: "metadata",
    icon: Database,
    title: "web.admin.common.libraryEditor.section.metadata",
    description: "web.admin.common.libraryEditor.section.metadataDescription",
  },
  {
    id: "advanced",
    icon: SlidersHorizontal,
    title: "web.admin.common.libraryEditor.section.advanced",
    description: "web.admin.common.libraryEditor.section.advancedDescription",
  },
];

function sectionForErrors(errors: LibraryFormErrors): SectionId | null {
  if (errors.name) return "general";
  if (errors.paths) return "folders";
  return null;
}

export interface LibraryEditorDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  library: Library | null;
  chapterThumbnailsSupported: boolean;
  /** Undefined when the server does not offer seek previews. */
  trickplaySupported?: boolean;
}

export function LibraryEditorDialog({
  open,
  onOpenChange,
  library,
  chapterThumbnailsSupported,
  trickplaySupported,
}: LibraryEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* Below sm the editor fills the screen; from sm up it is a centered box. */}
      <DialogContent
        className={cn(
          "flex h-[min(40rem,calc(100dvh-4rem))] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl",
          "max-sm:inset-0 max-sm:h-dvh max-sm:max-h-none max-sm:w-dvw max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none max-sm:border-0",
          // The header carries its own 44px close button on a phone.
          "max-sm:[&>[data-slot=dialog-close]]:hidden",
        )}
      >
        <LibraryEditorBody
          key={library?.id ?? "new"}
          library={library}
          chapterThumbnailsSupported={chapterThumbnailsSupported}
          trickplaySupported={trickplaySupported}
          onClose={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

function LibraryEditorBody({
  library,
  chapterThumbnailsSupported,
  trickplaySupported,
  onClose,
}: {
  library: Library | null;
  chapterThumbnailsSupported: boolean;
  trickplaySupported?: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const [section, setSection] = useState<SectionId>("general");
  const form = useLibraryForm({ library, onClose });
  // Radix needs the orientation as a prop for arrow-key navigation; the layout
  // itself switches at the same breakpoint (Tailwind's sm) through classes.
  const verticalTabs = useMediaQuery("(min-width: 40rem)", true);
  const triggerRefs = useRef(new Map<SectionId, HTMLButtonElement>());

  const sections = SECTIONS.filter(
    ({ id }) =>
      id !== "advanced" ||
      form.settingSupport.chapterThumbnails ||
      form.settingSupport.introDetection,
  );
  const activeSection = sections.some(({ id }) => id === section) ? section : "general";

  // Keep the active tab visible in the scrolling strip, including when a
  // failed save switches to the section with the error.
  useEffect(() => {
    triggerRefs.current
      .get(activeSection)
      ?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [activeSection]);

  const typeMeta = libraryTypeMeta(form.type);
  const folderCount = form.paths.filter((p) => p.trim()).length;
  const errorSections = new Set<SectionId>();
  if (form.errors.name) errorSections.add("general");
  if (form.errors.paths) errorSections.add("folders");

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const result = form.submit();
    if (!result.ok) {
      const target = sectionForErrors(result.errors);
      if (target) setSection(target);
    }
  }

  return (
    <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
      <DialogHeader className="border-border shrink-0 border-b ps-4 pe-2 pt-[calc(0.75rem+env(safe-area-inset-top))] pb-3 sm:px-6 sm:pe-12 sm:pt-4 sm:pb-4">
        <div className="flex items-center gap-3">
          <div className="border-border bg-surface text-primary flex size-10 shrink-0 items-center justify-center rounded-xl border">
            <typeMeta.icon className="size-5" />
          </div>
          <div className="min-w-0 flex-1 space-y-0.5 text-start">
            <DialogTitle>
              {library
                ? t("web.admin.common.libraryEditor.dialog.editTitle")
                : t("web.admin.common.libraryEditor.dialog.addTitle")}
            </DialogTitle>
            <DialogDescription className="text-xs break-words sm:truncate">
              {library
                ? t("web.admin.common.libraryEditor.dialog.editDescription", { name: library.name })
                : t("web.admin.common.libraryEditor.dialog.addDescription")}
            </DialogDescription>
          </div>
          <DialogClose asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="text-muted-foreground size-11 shrink-0 sm:hidden"
            >
              <XIcon className="size-5" />
              <span className="sr-only">{t("web.ui.close")}</span>
            </Button>
          </DialogClose>
        </div>
      </DialogHeader>

      <Tabs
        value={activeSection}
        onValueChange={(value) => setSection(value as SectionId)}
        orientation={verticalTabs ? "vertical" : "horizontal"}
        className="min-h-0 flex-1 gap-0"
      >
        <div className="border-border shrink-0 overflow-x-auto border-b max-sm:[scrollbar-width:none] sm:overflow-x-visible sm:overflow-y-auto sm:border-e sm:border-b-0 max-sm:[&::-webkit-scrollbar]:hidden">
          <TabsList className="w-max justify-start gap-1 rounded-none bg-transparent px-2 py-1.5 group-data-[orientation=horizontal]/tabs:h-auto sm:w-44 sm:flex-col sm:items-stretch sm:p-3">
            {sections.map(({ id, title, icon: Icon }) => (
              <TabsTrigger
                key={id}
                value={id}
                ref={(el) => {
                  if (el) triggerRefs.current.set(id, el);
                  else triggerRefs.current.delete(id);
                }}
                className="h-11 shrink-0 gap-2 rounded-lg px-3 py-2 sm:h-auto sm:justify-start sm:gap-2.5"
              >
                <Icon className="size-4 shrink-0" />
                <span>{t(title)}</span>
                {errorSections.has(id) ? (
                  <span className="bg-destructive size-1.5 shrink-0 rounded-full sm:ms-auto" />
                ) : id === "folders" && folderCount > 0 ? (
                  <span className="text-muted-foreground font-mono text-[10px] tabular-nums sm:ms-auto">
                    {folderCount}
                  </span>
                ) : null}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
        <div className="overlay-scroll min-h-0 flex-1 overflow-y-auto">
          {sections.map(({ id, title, description }) => (
            <TabsContent
              key={id}
              value={id}
              className="animate-in fade-in-0 slide-in-from-right-1 px-4 py-5 duration-200 sm:px-6"
            >
              <div className="mb-5 space-y-1">
                <h3 className="text-sm font-semibold">{t(title)}</h3>
                <p className="text-muted-foreground text-xs">{t(description)}</p>
              </div>
              {id === "general" && (
                <GeneralFields
                  form={form}
                  posterSlot={library ? <LibraryPosterSection library={library} /> : null}
                />
              )}
              {id === "folders" && (
                <div className="space-y-5">
                  <FolderFields form={form} />
                  <RealtimeMonitoringFields form={form} />
                </div>
              )}
              {id === "metadata" && <MetadataFields form={form} />}
              {id === "advanced" && (
                <AdvancedFields
                  form={form}
                  chapterThumbnailsSupported={chapterThumbnailsSupported}
                  trickplaySupported={trickplaySupported}
                />
              )}
            </TabsContent>
          ))}
        </div>
      </Tabs>

      <div className="border-border flex shrink-0 flex-wrap items-center justify-end gap-2 border-t px-4 pt-3 pb-[calc(0.75rem+env(safe-area-inset-bottom))] sm:flex-nowrap sm:px-6 sm:pt-4 sm:pb-4">
        {errorSections.size > 0 ? (
          <p className="text-destructive basis-full text-xs sm:me-auto sm:basis-auto">
            {[form.errors.name, form.errors.paths].filter(Boolean).join(" ")}
          </p>
        ) : null}
        <DialogClose asChild>
          <Button type="button" variant="ghost" className="h-11 sm:h-9">
            {t("common.cancel")}
          </Button>
        </DialogClose>
        <Button type="submit" disabled={form.isPending} className="h-11 flex-1 sm:h-9 sm:flex-none">
          {form.isPending
            ? library
              ? t("web.admin.common.libraryEditor.dialog.saving")
              : t("web.admin.common.libraryEditor.dialog.creating")
            : library
              ? t("web.admin.common.libraryEditor.dialog.saveChanges")
              : t("web.admin.common.libraryEditor.dialog.create")}
        </Button>
      </div>
    </form>
  );
}
