import { useSyncExternalStore } from "react";

const STORAGE_KEY = "silo.admin.restart_required";
const CHANGE_EVENT = "silo-admin-restart-required-change";

function canUseStorage() {
  return typeof window !== "undefined" && typeof window.localStorage !== "undefined";
}

function readStoredRestartRequired() {
  if (!canUseStorage()) {
    return false;
  }

  try {
    return window.localStorage.getItem(STORAGE_KEY) === "true";
  } catch {
    return false;
  }
}

function emitChange() {
  if (typeof window === "undefined") {
    return;
  }

  window.dispatchEvent(new Event(CHANGE_EVENT));
}

export function markAdminRestartRequired() {
  if (!canUseStorage()) {
    return;
  }

  try {
    window.localStorage.setItem(STORAGE_KEY, "true");
    emitChange();
  } catch {
    // Browsers can reject storage writes in private or restricted contexts.
  }
}

export function clearAdminRestartRequired() {
  if (!canUseStorage()) {
    return;
  }

  try {
    window.localStorage.removeItem(STORAGE_KEY);
    emitChange();
  } catch {
    // Browsers can reject storage writes in private or restricted contexts.
  }
}

function subscribe(callback: () => void) {
  if (typeof window === "undefined") {
    return () => {};
  }

  window.addEventListener(CHANGE_EVENT, callback);
  window.addEventListener("storage", callback);
  return () => {
    window.removeEventListener(CHANGE_EVENT, callback);
    window.removeEventListener("storage", callback);
  };
}

export function useAdminRestartRequired() {
  return useSyncExternalStore(subscribe, readStoredRestartRequired, () => false);
}
