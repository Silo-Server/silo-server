import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { toast } from "sonner";

import type { SkippableStep } from "./setupStorage";
import { useWizardContext } from "./WizardContext";

interface SaveableForm {
  dirtyCount: number;
  isSaving: boolean;
  save: () => Promise<void>;
}

/**
 * The submit protocol every settings step shares: save what is dirty, then mark
 * the step done, so a failed write keeps the admin on the step with their edits
 * intact. Skipping leaves the edits unsaved. `beforeDone` runs before the step
 * is done either way, for work already saved settings still need; resolving
 * false keeps the step open without an error (it unmounted).
 */
export function useStepSubmit(
  step: SkippableStep,
  form: SaveableForm,
  errorMessage: string,
  beforeDone?: () => Promise<boolean>,
) {
  const { markDone } = useWizardContext();
  const [submitting, setSubmitting] = useState(false);

  async function finish(save: boolean) {
    setSubmitting(true);
    try {
      if (save && form.dirtyCount > 0) await form.save();
      if ((await beforeDone?.()) === false) return;
      markDone(step);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : errorMessage);
    } finally {
      setSubmitting(false);
    }
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    return finish(true);
  }

  return {
    handleSubmit,
    busy: submitting || form.isSaving,
    skip: () => (beforeDone ? void finish(false) : markDone(step)),
  };
}

/** Publishes a step's one-line summary to the rail whenever it changes. */
export function useStepSummary(step: SkippableStep, summary: string | undefined) {
  const { setSummary } = useWizardContext();
  useEffect(() => {
    setSummary(step, summary);
  }, [step, summary, setSummary]);
}
