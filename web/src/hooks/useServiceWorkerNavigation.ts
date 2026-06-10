import { useEffect } from "react";
import { useNavigate } from "react-router";

/**
 * Listens for "notification-click" messages posted by the service worker
 * (web/public/sw.js) when a notification is clicked while an app tab is open.
 * The SW focuses the existing tab and posts the deep-link target; without this
 * listener the warm-focus path would silently no-op instead of navigating.
 */
export function useServiceWorkerNavigation() {
  const navigate = useNavigate();
  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    const handler = (event: MessageEvent) => {
      const data = event.data as { type?: string; link?: string } | undefined;
      if (
        data?.type === "notification-click" &&
        typeof data.link === "string" &&
        data.link.length > 0
      ) {
        navigate(data.link);
      }
    };
    navigator.serviceWorker.addEventListener("message", handler);
    return () => navigator.serviceWorker.removeEventListener("message", handler);
  }, [navigate]);
}
