import { useState } from "react";
import type { FormEvent } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";

import type { Library } from "@/api/types";
import { FolderFields, GeneralFields } from "@/components/admin/libraries/LibraryFormSections";
import { libraryTypeMeta } from "@/components/admin/libraries/libraryTypes";
import { useLibraryForm } from "@/components/admin/libraries/useLibraryForm";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { useDeleteLibrary } from "@/hooks/queries/admin/libraries";

import { librarySummary } from "../librarySummary";
import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSummary } from "../useStep";
import { useWizardContext } from "../WizardContext";

/**
 * Name, type, and folders for one library. Creating and editing share the
 * form: with `library` set it saves changes, otherwise it adds a new one.
 */
function LibraryEditor({
  library,
  onDone,
  onCancel,
}: {
  library: Library | null;
  onDone: () => void;
  onCancel?: () => void;
}) {
  const form = useLibraryForm({
    library,
    onSaved: onDone,
    onClose: onDone,
    resetAfterCreate: true,
  });

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    form.submit();
  }

  return (
    <form
      onSubmit={handleSubmit}
      className="setup-section setup-section-padded setup-library-editor"
    >
      <h2 className="setup-section-title">{library ? `Edit ${library.name}` : "New library"}</h2>
      <GeneralFields form={form} />
      <div className="space-y-2">
        <p className="text-[13px] font-medium">Folders</p>
        <FolderFields form={form} />
      </div>
      <div className="flex flex-wrap items-center gap-3 pt-1">
        <Button type="submit" variant="secondary" disabled={form.isPending}>
          {form.isPending
            ? library
              ? "Saving…"
              : "Adding…"
            : library
              ? "Save changes"
              : "Add library"}
        </Button>
        {onCancel ? (
          <Button type="button" variant="ghost" onClick={onCancel} disabled={form.isPending}>
            Cancel
          </Button>
        ) : null}
      </div>
    </form>
  );
}

type EditorState = { mode: "closed" } | { mode: "new" } | { mode: "edit"; library: Library };

export function LibraryStep() {
  const { libraries, librariesLoading, markDone, refetchLibraries } = useWizardContext();
  const deleteLibrary = useDeleteLibrary();
  const [editor, setEditor] = useState<EditorState>({ mode: "closed" });
  const [pendingDelete, setPendingDelete] = useState<Library | null>(null);

  // With nothing added yet the form is simply open; once one library exists it
  // folds behind "Add another".
  const activeEditor: EditorState =
    editor.mode === "closed" && libraries.length === 0 ? { mode: "new" } : editor;

  useStepSummary("library", libraries.length === 0 ? undefined : librarySummary(libraries));

  function handleDone() {
    refetchLibraries();
    setEditor({ mode: "closed" });
  }

  function confirmDelete() {
    if (!pendingDelete) return;
    deleteLibrary.mutate(pendingDelete.id, {
      onSuccess: () => {
        setPendingDelete(null);
        if (editor.mode === "edit" && editor.library.id === pendingDelete.id) {
          setEditor({ mode: "closed" });
        }
        refetchLibraries();
      },
    });
  }

  if (librariesLoading) return <StepSkeleton rows={2} />;

  return (
    <StepFrame
      title="Add your media"
      lede="Point Silo at the folders your files live in. Each library holds one kind of media and scans on its own schedule."
      onContinue={() => markDone("library")}
      // An open editor holds edits Continue would not submit; the editor's
      // own Add / Save / Cancel closes it first.
      disabled={libraries.length === 0 || activeEditor.mode !== "closed"}
      onSkip={() => markDone("library")}
      footnote={
        activeEditor.mode !== "closed" && libraries.length > 0
          ? "Save or cancel the open library editor to continue."
          : "Metadata languages, providers, and extras live in Admin › Libraries."
      }
    >
      {libraries.length > 0 ? (
        <StepSection title="Your libraries">
          {libraries.map((library) => {
            const meta = libraryTypeMeta(library.type);
            const Icon = meta.icon;
            const editing = activeEditor.mode === "edit" && activeEditor.library.id === library.id;
            return (
              <div
                key={library.id}
                className="setup-library-row"
                data-editing={editing || undefined}
              >
                <Icon className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{library.name}</p>
                  <p className="text-muted-foreground truncate text-xs">
                    {meta.label} · {library.paths.join(", ")}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-0.5">
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-foreground size-8"
                    aria-label={`Edit ${library.name}`}
                    aria-pressed={editing}
                    onClick={() =>
                      setEditor(editing ? { mode: "closed" } : { mode: "edit", library })
                    }
                  >
                    <Pencil className="size-3.5" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-destructive size-8"
                    aria-label={`Delete ${library.name}`}
                    onClick={() => setPendingDelete(library)}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </div>
              </div>
            );
          })}
        </StepSection>
      ) : null}

      {activeEditor.mode === "new" ? (
        <LibraryEditor
          library={null}
          onDone={handleDone}
          onCancel={libraries.length > 0 ? () => setEditor({ mode: "closed" }) : undefined}
        />
      ) : activeEditor.mode === "edit" ? (
        <LibraryEditor
          key={activeEditor.library.id}
          library={activeEditor.library}
          onDone={handleDone}
          onCancel={() => setEditor({ mode: "closed" })}
        />
      ) : (
        <button type="button" className="setup-add-row" onClick={() => setEditor({ mode: "new" })}>
          <Plus className="size-3.5" aria-hidden="true" />
          Add another library
        </button>
      )}

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
        title={pendingDelete ? `Delete ${pendingDelete.name}?` : "Delete library?"}
        description="Removes the library and everything Silo learned about it. Your files on disk are not touched."
        confirmLabel="Delete library"
        variant="destructive"
        onConfirm={confirmDelete}
        isPending={deleteLibrary.isPending}
      />
    </StepFrame>
  );
}
