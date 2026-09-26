import { V2ProblemError } from "@/api/v2/request";
import type { AddPayload } from "@/components/RecipeGallery/RecipeConfigDrawer";

export interface AdminSectionCreationTarget {
  libraryID: number;
  collectionID?: string;
  sectionID?: string;
  status:
    | "pending"
    | "importing"
    | "creating"
    | "complete"
    | "import_unknown"
    | "section_failed"
    | "section_unknown";
  error?: string;
}

export interface AdminSectionCreation {
  payload: Readonly<AddPayload>;
  targets: readonly AdminSectionCreationTarget[];
}

interface AdminSectionCreationActions {
  resolveCollection(payload: Readonly<AddPayload>, libraryID: number): Promise<string>;
  createSection(
    payload: Readonly<AddPayload>,
    libraryID: number,
    collectionID: string,
  ): Promise<string>;
  onProgress?(state: AdminSectionCreation): void;
}

export function createAdminSectionCreation(
  payload: AddPayload,
  libraryIDs: readonly number[],
): AdminSectionCreation {
  const ids = [...new Set(libraryIDs)];
  if (ids.length === 0 || ids.length > 100 || ids.some((id) => !Number.isInteger(id) || id <= 0)) {
    throw new Error("Choose between 1 and 100 libraries before creating sections");
  }
  return {
    payload: structuredClone(payload),
    targets: ids.map((libraryID) => ({ libraryID, status: "pending" })),
  };
}

function failureMessage(error: unknown): string {
  return error instanceof Error ? error.message : "The operation did not finish";
}

/**
 * One explicit user attempt. Imported IDs are published before section creation,
 * and each completed library is retained if a later library fails. An import
 * failure may have committed remotely, so another attempt never repeats it.
 * Section creation is retried only after an explicit server rejection; unknown
 * outcomes require reconciliation with the section list before further action.
 */
export async function runAdminSectionCreation(
  initial: AdminSectionCreation,
  actions: AdminSectionCreationActions,
): Promise<AdminSectionCreation> {
  let state = initial;
  const update = (index: number, changes: Partial<AdminSectionCreationTarget>) => {
    state = {
      ...state,
      targets: state.targets.map((target, i) => (i === index ? { ...target, ...changes } : target)),
    };
    actions.onProgress?.(state);
  };

  for (let index = 0; index < state.targets.length; index++) {
    const target = state.targets[index];
    if (!target) continue;
    if (target.status !== "pending" && target.status !== "section_failed") continue;
    let collectionID = target.collectionID;
    if (!collectionID) {
      update(index, { status: "importing", error: undefined });
      try {
        collectionID = await actions.resolveCollection(state.payload, target.libraryID);
        if (!collectionID) throw new Error("The import did not return a collection ID");
      } catch (error) {
        update(index, { status: "import_unknown", error: failureMessage(error) });
        return state;
      }
      // This update is deliberately before createSection: UI state must retain
      // the imported collection even when that subsequent request fails.
      update(index, { collectionID, status: "creating" });
    } else {
      update(index, { status: "creating", error: undefined });
    }
    try {
      const sectionID = await actions.createSection(state.payload, target.libraryID, collectionID);
      if (!sectionID) throw new Error("The create request did not return a section ID");
      update(index, { sectionID, status: "complete", error: undefined });
    } catch (error) {
      const rejected =
        error instanceof V2ProblemError &&
        [400, 401, 403, 404, 409, 412, 422, 428, 429].includes(error.status);
      update(index, {
        status: rejected ? "section_failed" : "section_unknown",
        error: failureMessage(error),
      });
      return state;
    }
  }
  return state;
}
