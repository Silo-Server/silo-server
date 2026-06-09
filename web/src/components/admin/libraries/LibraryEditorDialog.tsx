import { useState } from "react";
import type { FormEvent } from "react";
import { Database, FolderOpen, Settings2, SlidersHorizontal } from "lucide-react";

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
import { cn } from "@/lib/utils";

import { AdvancedFields, FolderFields, GeneralFields, MetadataFields } from "./LibraryFormSections";
import { libraryTypeMeta } from "./libraryTypes";
import { LibraryPosterSection } from "./LibraryPosterSection";
import { useLibraryForm } from "./useLibraryForm";
import type { LibraryFormErrors } from "./useLibraryForm";

type SectionId = "general" | "folders" | "metadata" | "advanced";

const SECTIONS: Array<{
  id: SectionId;
  label: string;
  icon: typeof Settings2;
  title: string;
  description: string;
}> = [
  {
    id: "general",
    label: "General",
    icon: Settings2,
    title: "General",
    description: "Name this library and choose the kind of media it holds.",
  },
  {
    id: "folders",
    label: "Folders",
    icon: FolderOpen,
    title: "Folders",
    description: "Silo scans these folders for media and watches them for changes.",
  },
  {
    id: "metadata",
    label: "Metadata",
    icon: Database,
    title: "Metadata",
    description: "Control where artwork and descriptions come from, and in which language.",
  },
  {
    id: "advanced",
    label: "Advanced",
    icon: SlidersHorizontal,
    title: "Advanced",
    description: "Optional background processing for this library.",
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
}

export function LibraryEditorDialog({
  open,
  onOpenChange,
  library,
  chapterThumbnailsSupported,
}: LibraryEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex h-[min(40rem,calc(100dvh-4rem))] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        <LibraryEditorBody
          key={library?.id ?? "new"}
          library={library}
          chapterThumbnailsSupported={chapterThumbnailsSupported}
          onClose={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

function LibraryEditorBody({
  library,
  chapterThumbnailsSupported,
  onClose,
}: {
  library: Library | null;
  chapterThumbnailsSupported: boolean;
  onClose: () => void;
}) {
  const [section, setSection] = useState<SectionId>("general");
  const form = useLibraryForm({ library, onClose });

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
      <DialogHeader className="border-border shrink-0 border-b px-5 py-4 pr-12 sm:px-6 sm:pr-12">
        <div className="flex items-center gap-3">
          <div className="border-border bg-surface text-primary flex size-10 shrink-0 items-center justify-center rounded-xl border">
            <typeMeta.icon className="size-5" />
          </div>
          <div className="min-w-0 space-y-0.5 text-left">
            <DialogTitle>{library ? "Edit Library" : "Add Library"}</DialogTitle>
            <DialogDescription className="truncate text-xs">
              {library
                ? `Configure how “${library.name}” is scanned and matched.`
                : "Set up a new library from folders on your server."}
            </DialogDescription>
          </div>
        </div>
      </DialogHeader>

      <Tabs
        value={section}
        onValueChange={(value) => setSection(value as SectionId)}
        orientation="vertical"
        className="min-h-0 flex-1 gap-0"
      >
        <div className="border-border shrink-0 overflow-y-auto border-r">
          <TabsList className="w-13 flex-col items-stretch justify-start gap-1 rounded-none bg-transparent p-2 sm:w-44 sm:p-3">
            {SECTIONS.map(({ id, label, icon: Icon }) => (
              <TabsTrigger
                key={id}
                value={id}
                className="h-auto shrink-0 justify-start gap-2.5 rounded-lg px-2.5 py-2 sm:px-3"
              >
                <Icon className="size-4 shrink-0" />
                <span className="hidden sm:inline">{label}</span>
                {errorSections.has(id) ? (
                  <span
                    className={cn(
                      "bg-destructive size-1.5 rounded-full",
                      "absolute top-1 right-1 sm:static sm:ml-auto",
                    )}
                  />
                ) : id === "folders" && folderCount > 0 ? (
                  <span className="text-muted-foreground ml-auto hidden font-mono text-[10px] tabular-nums sm:inline">
                    {folderCount}
                  </span>
                ) : null}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
        <div className="overlay-scroll min-h-0 flex-1 overflow-y-auto">
          {SECTIONS.map(({ id, title, description }) => (
            <TabsContent
              key={id}
              value={id}
              className="animate-in fade-in-0 slide-in-from-right-1 px-5 py-5 duration-200 sm:px-6"
            >
              <div className="mb-5 space-y-1">
                <h3 className="text-sm font-semibold">{title}</h3>
                <p className="text-muted-foreground text-xs">{description}</p>
              </div>
              {id === "general" && (
                <GeneralFields
                  form={form}
                  posterSlot={library ? <LibraryPosterSection library={library} /> : null}
                />
              )}
              {id === "folders" && <FolderFields form={form} />}
              {id === "metadata" && <MetadataFields form={form} />}
              {id === "advanced" && (
                <AdvancedFields
                  form={form}
                  chapterThumbnailsSupported={chapterThumbnailsSupported}
                />
              )}
            </TabsContent>
          ))}
        </div>
      </Tabs>

      <div className="border-border flex shrink-0 items-center justify-end gap-2 border-t px-5 py-4 sm:px-6">
        {errorSections.size > 0 ? (
          <p className="text-destructive mr-auto text-xs">
            {[form.errors.name, form.errors.paths].filter(Boolean).join(" ")}
          </p>
        ) : null}
        <DialogClose asChild>
          <Button type="button" variant="ghost">
            Cancel
          </Button>
        </DialogClose>
        <Button type="submit" disabled={form.isPending}>
          {form.isPending
            ? library
              ? "Saving…"
              : "Creating…"
            : library
              ? "Save Changes"
              : "Create Library"}
        </Button>
      </div>
    </form>
  );
}
