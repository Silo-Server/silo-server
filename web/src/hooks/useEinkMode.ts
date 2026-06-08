import { useEffect, useState } from "react";

const STORAGE_KEY = "ebooks.eink.enabled";
const CLASS_NAME = "eink";

export function useEinkMode(): [boolean, (enabled: boolean) => void] {
  const [enabled, setEnabled] = useState<boolean>(() => {
    try {
      return window.localStorage.getItem(STORAGE_KEY) === "true";
    } catch {
      return false;
    }
  });

  useEffect(() => {
    if (enabled) {
      document.body.classList.add(CLASS_NAME);
    } else {
      document.body.classList.remove(CLASS_NAME);
    }
  }, [enabled]);

  const set = (next: boolean) => {
    setEnabled(next);
    try {
      window.localStorage.setItem(STORAGE_KEY, next ? "true" : "false");
    } catch {
      // Storage unavailable.
    }
  };

  return [enabled, set];
}
