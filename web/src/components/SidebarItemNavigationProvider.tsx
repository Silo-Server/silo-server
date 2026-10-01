import type { ReactNode } from "react";
import {
  SidebarItemNavigationContext,
  SidebarItemDetailsReadyContext,
  SidebarItemEnteredFromHomeContext,
  type BeginSidebarItemNavigation,
} from "@/components/sidebarItemNavigationContext";

export default function SidebarItemNavigationProvider({
  begin,
  itemDetailsReady,
  enteredItemFromHome = false,
  children,
}: {
  begin: BeginSidebarItemNavigation;
  itemDetailsReady: boolean;
  enteredItemFromHome?: boolean;
  children: ReactNode;
}) {
  return (
    <SidebarItemNavigationContext.Provider value={begin}>
      <SidebarItemDetailsReadyContext.Provider value={itemDetailsReady}>
        <SidebarItemEnteredFromHomeContext.Provider value={enteredItemFromHome}>
          {children}
        </SidebarItemEnteredFromHomeContext.Provider>
      </SidebarItemDetailsReadyContext.Provider>
    </SidebarItemNavigationContext.Provider>
  );
}
