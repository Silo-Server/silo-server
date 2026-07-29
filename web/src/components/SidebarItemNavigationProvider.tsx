import type { ReactNode } from "react";
import {
  SidebarItemNavigationContext,
  SidebarItemDetailsReadyContext,
  type BeginSidebarItemNavigation,
} from "@/components/sidebarItemNavigationContext";

export default function SidebarItemNavigationProvider({
  begin,
  itemDetailsReady,
  children,
}: {
  begin: BeginSidebarItemNavigation;
  itemDetailsReady: boolean;
  children: ReactNode;
}) {
  return (
    <SidebarItemNavigationContext.Provider value={begin}>
      <SidebarItemDetailsReadyContext.Provider value={itemDetailsReady}>
        {children}
      </SidebarItemDetailsReadyContext.Provider>
    </SidebarItemNavigationContext.Provider>
  );
}
