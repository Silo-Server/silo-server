import { createContext, useContext } from "react";
import type { SidebarItemNavigationRequest } from "@/components/sidebarItemNavigation";

export type BeginSidebarItemNavigation = (request: SidebarItemNavigationRequest) => boolean;

export const SidebarItemNavigationContext = createContext<BeginSidebarItemNavigation | null>(null);
export const SidebarItemDetailsReadyContext = createContext(true);

export function useSidebarItemNavigation(): BeginSidebarItemNavigation | null {
  return useContext(SidebarItemNavigationContext);
}

export function useSidebarItemDetailsReady(): boolean {
  return useContext(SidebarItemDetailsReadyContext);
}
