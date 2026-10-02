export function updateCheckboxSelection(
  selectedIds: ReadonlySet<string>,
  orderedIds: readonly string[],
  anchorId: string | null,
  targetId: string,
  checked: boolean,
  extendRange: boolean,
  selectionIdFor: (orderedId: string) => string = (orderedId) => orderedId,
): Set<string> {
  const next = new Set(selectedIds);
  const anchorIndex = anchorId === null ? -1 : orderedIds.indexOf(anchorId);
  const targetIndex = orderedIds.indexOf(targetId);
  const idsToUpdate =
    extendRange && anchorIndex >= 0 && targetIndex >= 0
      ? orderedIds.slice(Math.min(anchorIndex, targetIndex), Math.max(anchorIndex, targetIndex) + 1)
      : [targetId];

  for (const id of idsToUpdate) {
    const selectionId = selectionIdFor(id);
    if (checked) {
      next.add(selectionId);
    } else {
      next.delete(selectionId);
    }
  }

  return next;
}
