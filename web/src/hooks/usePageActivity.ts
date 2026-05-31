import { useEffect, useState } from "react";

export interface PageActivityState {
  isVisible: boolean;
  isFocused: boolean;
  isFrozen: boolean;
  canPollDashboard: boolean;
  canApplyRealtimeUpdates: boolean;
}

function readPageActivity(): PageActivityState {
  const isVisible = typeof document === "undefined" ? true : document.visibilityState === "visible";
  const isFocused = typeof document === "undefined" ? true : document.hasFocus();
  const isFrozen = false;

  return {
    isVisible,
    isFocused,
    isFrozen,
    canPollDashboard: isVisible && isFocused && !isFrozen,
    canApplyRealtimeUpdates: isVisible && !isFrozen,
  };
}

export function usePageActivity() {
  const [state, setState] = useState<PageActivityState>(() => readPageActivity());

  useEffect(() => {
    let frozen = false;

    const update = () => {
      const next = readPageActivity();
      setState({
        ...next,
        isFrozen: frozen,
        canPollDashboard: next.isVisible && next.isFocused && !frozen,
        canApplyRealtimeUpdates: next.isVisible && !frozen,
      });
    };

    const handleFreeze = () => {
      frozen = true;
      update();
    };
    const handleResume = () => {
      frozen = false;
      update();
    };
    const handlePageShow = () => {
      frozen = false;
      update();
    };
    const handlePageHide = () => {
      update();
    };

    window.addEventListener("focus", update);
    window.addEventListener("blur", update);
    window.addEventListener("pageshow", handlePageShow);
    window.addEventListener("pagehide", handlePageHide);
    document.addEventListener("visibilitychange", update);
    document.addEventListener("freeze", handleFreeze);
    document.addEventListener("resume", handleResume);

    update();

    return () => {
      window.removeEventListener("focus", update);
      window.removeEventListener("blur", update);
      window.removeEventListener("pageshow", handlePageShow);
      window.removeEventListener("pagehide", handlePageHide);
      document.removeEventListener("visibilitychange", update);
      document.removeEventListener("freeze", handleFreeze);
      document.removeEventListener("resume", handleResume);
    };
  }, []);

  return state;
}
