import { useEffect, useState } from "react";
import { api } from "@/api/client";

interface Library {
  id: number;
  name: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onConfirm: (libraryIDs: number[]) => void | Promise<void>;
}

export default function BulkApplyDialog({ open, onClose, onConfirm }: Props) {
  const [libraries, setLibraries] = useState<Library[]>([]);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open) return;
    api<Library[]>("/libraries")
      .then((j) => {
        const libs = Array.isArray(j) ? j : [];
        setLibraries(libs);
        setSelected(new Set(libs.map((l) => l.id)));
      })
      .catch((err) => {
        console.error("BulkApplyDialog: failed to load libraries", err);
      });
  }, [open]);

  async function handleConfirm() {
    setSubmitting(true);
    try {
      await onConfirm(Array.from(selected));
    } catch {
      // The caller owns user-facing error reporting. Keep the dialog open so
      // the selection can be retried.
    } finally {
      setSubmitting(false);
    }
  }

  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
      <div className="w-[420px] rounded-xl border border-white/10 bg-zinc-900 p-5">
        <h3 className="mb-3 text-sm font-semibold">Apply to which libraries?</h3>
        <div className="max-h-[40vh] space-y-2 overflow-y-auto">
          {libraries.map((l) => (
            <label key={l.id} className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selected.has(l.id)}
                onChange={(e) => {
                  const next = new Set(selected);
                  if (e.target.checked) next.add(l.id);
                  else next.delete(l.id);
                  setSelected(next);
                }}
              />
              {l.name}
            </label>
          ))}
        </div>
        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="rounded border border-white/15 px-3 py-1 text-sm"
          >
            Cancel
          </button>
          <button
            type="button"
            disabled={selected.size === 0 || submitting}
            onClick={() => void handleConfirm()}
            className="rounded bg-indigo-600 px-3 py-1 text-sm text-white disabled:opacity-50"
          >
            {submitting ? "Applying..." : `Apply (${selected.size})`}
          </button>
        </div>
      </div>
    </div>
  );
}
