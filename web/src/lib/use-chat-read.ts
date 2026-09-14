import { useEffect, useSyncExternalStore } from "react";
import { control } from "./store";

function subscribeForeground(listener: () => void) {
  document.addEventListener("visibilitychange", listener);
  window.addEventListener("focus", listener);
  window.addEventListener("blur", listener);
  return () => {
    document.removeEventListener("visibilitychange", listener);
    window.removeEventListener("focus", listener);
    window.removeEventListener("blur", listener);
  };
}

export function useChatForeground() {
  return useSyncExternalStore(
    subscribeForeground,
    () => document.visibilityState === "visible" && document.hasFocus(),
    () => false,
  );
}

// A receipt covers rendered messages, not inventory that may precede replay.
export function useChatRead(
  id: string,
  cursor: number,
  readCursor: number,
  ready: boolean,
) {
  const foreground = useChatForeground();
  useEffect(() => {
    if (!foreground || !ready || cursor <= readCursor) return;
    let disposed = false;
    let retry: ReturnType<typeof setTimeout> | undefined;
    const acknowledge = () => {
      if (
        disposed ||
        document.visibilityState !== "visible" ||
        !document.hasFocus()
      )
        return;
      void control.markRead(id, cursor).catch(() => {
        if (!disposed) retry = setTimeout(acknowledge, 3000);
      });
    };
    acknowledge();
    return () => {
      disposed = true;
      clearTimeout(retry);
    };
  }, [id, cursor, readCursor, ready, foreground]);
}
