import { useEffect, useRef } from "react";

/** A ref that is true while the component is mounted, for async work to check before it updates. */
export function useMounted() {
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return mounted;
}
