import { LibraryForm } from "@/components/admin/libraries/LibraryForm";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";
import { useWizardContext } from "../WizardContext";

export function LibraryStep() {
  const { libraries, markDone, refetchLibraries } = useWizardContext();

  function handleSkip() {
    markDone("library");
    toast.success("Library setup skipped");
  }

  function handleLibrarySaved() {
    refetchLibraries();
  }

  return (
    <div className="space-y-5">
      {libraries.length > 0 && (
        <div className="border-foreground/[0.07] bg-foreground/[0.03] space-y-2 rounded-xl border p-4">
          <p className="text-muted-foreground text-xs font-semibold tracking-[0.1em] uppercase">
            Added libraries
          </p>
          <div className="space-y-2">
            {libraries.map((library) => (
              <div key={library.id} className="text-sm">
                <div className="font-medium">{library.name}</div>
                <div className="text-muted-foreground text-xs">
                  {library.type} · {library.paths.length}{" "}
                  {library.paths.length === 1 ? "path" : "paths"}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      <LibraryForm
        library={null}
        chapterThumbnailsSupported={libraries[0]?.chapter_thumbnails_supported ?? true}
        onSaved={handleLibrarySaved}
        resetAfterCreate
        submitLabel="Add library"
        savingLabel="Adding..."
      />

      <div className="flex gap-3 pt-3">
        <Button type="button" onClick={() => markDone("library")} disabled={libraries.length === 0}>
          Continue
        </Button>
        <Button type="button" variant="ghost" onClick={handleSkip}>
          Skip
        </Button>
      </div>
    </div>
  );
}
