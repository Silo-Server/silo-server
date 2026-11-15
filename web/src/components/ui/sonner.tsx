import { useLayoutEffect, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { Direction } from "radix-ui";
import { Toaster as SonnerToaster, type ToasterProps } from "sonner";

/**
 * Only the fullscreen element and its descendants show while the page is
 * fullscreen, so the toaster's host moves into it and back out. Moving the host
 * node keeps the toaster mounted, so toasts already on screen stay.
 */
function useToasterHost() {
  const [host] = useState(() => document.createElement("div"));
  useLayoutEffect(() => {
    const place = () => (document.fullscreenElement ?? document.body).append(host);
    place();
    document.addEventListener("fullscreenchange", place);
    return () => {
      document.removeEventListener("fullscreenchange", place);
      host.remove();
    };
  }, [host]);
  return host;
}

const Toaster = ({ ...props }: ToasterProps) => {
  const dir = Direction.useDirection();
  const host = useToasterHost();
  return createPortal(
    <SonnerToaster
      theme="dark"
      dir={dir}
      // Sonner's corners are physical; toasts sit at the bottom end corner.
      position={dir === "rtl" ? "bottom-left" : "bottom-right"}
      className="toaster group"
      toastOptions={{
        classNames: {
          toast: "!text-[var(--popover-foreground)]",
          title: "!text-[var(--popover-foreground)]",
          description: "!text-[var(--popover-foreground)]",
        },
      }}
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border)",
        } as CSSProperties
      }
      {...props}
    />,
    host,
  );
};

export { Toaster };
