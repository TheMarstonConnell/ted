import { useSearchParams } from "react-router-dom";

// Overlays share URL state but are mutually exclusive. In particular, opening
// project options must not navigate away from (or remount) the current chat.
export function usePanel() {
  const [params, setParams] = useSearchParams();
  return {
    params,
    open: (key: string, value: string) =>
      setParams((previous) => {
        const next = new URLSearchParams(previous);
        if (key === "dialog" || key === "panel" || key === "sidebar") {
          for (const overlay of ["dialog", "panel", "sidebar", "project"])
            next.delete(overlay);
        }
        next.set(key, value);
        return next;
      }),
    openProjectSettings: (projectId: string) =>
      setParams((previous) => {
        const next = new URLSearchParams(previous);
        next.delete("sidebar");
        next.delete("dialog");
        next.set("panel", "project-settings");
        next.set("project", projectId);
        return next;
      }),
    close: (key: string) =>
      setParams((previous) => {
        const next = new URLSearchParams(previous);
        next.delete(key);
        if (key === "panel") next.delete("project");
        return next;
      }),
  };
}
