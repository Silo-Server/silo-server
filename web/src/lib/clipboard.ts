/**
 * Copies text to the clipboard, falling back to a hidden textarea when the
 * async Clipboard API is unavailable — notably on insecure origins, which a
 * self-hosted server reached over plain HTTP on the LAN commonly is.
 */
export async function copyTextToClipboard(text: string): Promise<void> {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }

  // A modal dialog traps focus: a textarea outside it loses focus as soon as
  // it gets it, and execCommand("copy") then copies nothing while still
  // returning true. Mount the textarea inside the dialog that holds focus, or
  // the topmost open one when focus has fallen back to the body.
  const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const openDialogs = document.querySelectorAll(
    '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"]',
  );
  const host =
    previous?.closest('[role="dialog"], [role="alertdialog"]') ??
    openDialogs[openDialogs.length - 1] ??
    document.body;
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  host.appendChild(textarea);
  try {
    textarea.focus({ preventScroll: true });
    textarea.select();
    if (document.activeElement !== textarea || !document.execCommand("copy")) {
      throw new Error("Copy command failed");
    }
  } finally {
    textarea.remove();
    previous?.focus({ preventScroll: true });
  }
}
