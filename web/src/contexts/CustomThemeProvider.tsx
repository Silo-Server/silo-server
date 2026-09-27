import { useEffect, useRef } from "react";
import type { ReactNode } from "react";
import { useAdminPublicCss } from "@/hooks/queries/theme";

/** Reject values that could break out of CSS declarations. */
const UNSAFE_VALUE = /[;{}]|\/\*|<\//;

function buildVarOverrideCSS(vars: Record<string, string>): string {
  const entries = Object.entries(vars).filter(([, v]) => v !== "" && !UNSAFE_VALUE.test(v));
  if (entries.length === 0) return "";
  const props = entries.map(([k, v]) => `  --${k}: ${v};`).join("\n");
  return `:root {\n${props}\n}`;
}

function getOrCreateStyle(id: string): HTMLStyleElement {
  let el = document.getElementById(id) as HTMLStyleElement | null;
  if (!el) {
    el = document.createElement("style");
    el.id = id;
    document.head.appendChild(el);
  }
  return el;
}

/**
 * Paints the admin's server-wide theme customization — token overrides, then
 * raw CSS — on top of the Cinema Dark base. Theming is an admin decision:
 * profiles have no overrides of their own.
 */
export function CustomThemeProvider({ children }: { children: ReactNode }) {
  const { data: adminCss } = useAdminPublicCss();

  const adminVarsRef = useRef<HTMLStyleElement>(null);
  const adminRawRef = useRef<HTMLStyleElement>(null);

  // Create style elements on mount
  useEffect(() => {
    adminVarsRef.current = getOrCreateStyle("silo-admin-vars");
    adminRawRef.current = getOrCreateStyle("silo-admin-raw-css");

    return () => {
      // Clean up on unmount (dev HMR)
      adminVarsRef.current?.remove();
      adminRawRef.current?.remove();
    };
  }, []);

  useEffect(() => {
    if (adminVarsRef.current) {
      adminVarsRef.current.textContent = buildVarOverrideCSS(adminCss?.vars ?? {});
    }
  }, [adminCss?.vars]);

  useEffect(() => {
    if (adminRawRef.current) {
      adminRawRef.current.textContent = adminCss?.rawCss ?? "";
    }
  }, [adminCss?.rawCss]);

  return children;
}
