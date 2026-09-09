import { useEffect, useId, useState } from "react";
import { Check, Copy, GitBranch, LoaderCircle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import {
  api,
  type ProjectBranches,
  type Workspace,
  type WorkspaceSelection,
} from "@/lib/api";

const REPOSITORY_DEFAULT = "__repository_default__";

function useProjectBranches(projectId?: string) {
  const [state, setState] = useState<{
    projectId?: string;
    branches: ProjectBranches | null;
    error: string | null;
  }>({ projectId, branches: null, error: null });
  useEffect(() => {
    let active = true;
    if (!projectId) return () => undefined;
    void api<ProjectBranches>(
      `/v1/projects/${encodeURIComponent(projectId)}/branches`,
    )
      .then((branches) => {
        if (active) setState({ projectId, branches, error: null });
      })
      .catch((error) => {
        if (active)
          setState({ projectId, branches: null, error: String(error) });
      });
    return () => {
      active = false;
    };
  }, [projectId]);
  return state.projectId === projectId
    ? { ...state, loading: !!projectId && !state.branches && !state.error }
    : { projectId, branches: null, error: null, loading: !!projectId };
}

export function WorkspaceFields({
  projectId,
  value,
  onChange,
  disabled = false,
  compact = false,
}: {
  projectId?: string;
  value: WorkspaceSelection;
  onChange: (value: WorkspaceSelection) => void;
  disabled?: boolean;
  compact?: boolean;
}) {
  const locationId = useId();
  const branchId = useId();
  const { branches, error, loading } = useProjectBranches(projectId);
  const knownNonGit = branches?.is_git === false;
  const branchValue = value.base_branch || REPOSITORY_DEFAULT;
  const branchOptions = [...(branches?.branches || [])];
  if (value.base_branch && !branchOptions.includes(value.base_branch))
    branchOptions.unshift(value.base_branch);

  return (
    <div
      className={
        compact ? "flex min-w-0 flex-1 flex-wrap items-end gap-2" : "space-y-4"
      }
      role="group"
      aria-label="Workspace settings"
    >
      <div className="grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)] gap-2">
        <Label htmlFor={locationId}>Workspace</Label>
        <NativeSelect
          id={locationId}
          size={compact ? "sm" : "default"}
          className="w-full"
          value={value.mode}
          disabled={disabled || loading}
          onChange={(event) => {
            const mode = event.target.value as WorkspaceSelection["mode"];
            onChange(
              mode === "worktree"
                ? {
                    mode,
                    ...(value.base_branch
                      ? { base_branch: value.base_branch }
                      : {}),
                  }
                : { mode },
            );
          }}
        >
          <NativeSelectOption value="current_checkout">
            Current checkout
          </NativeSelectOption>
          <NativeSelectOption value="worktree" disabled={knownNonGit}>
            Worktree
          </NativeSelectOption>
        </NativeSelect>
      </div>
      {value.mode === "worktree" && (
        <div className="grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)] gap-2">
          <Label htmlFor={branchId}>Start from</Label>
          {projectId ? (
            <NativeSelect
              id={branchId}
              size={compact ? "sm" : "default"}
              className="w-full font-mono"
              value={branchValue}
              disabled={disabled || loading || knownNonGit || !!error}
              onChange={(event) =>
                onChange({
                  mode: "worktree",
                  ...(event.target.value === REPOSITORY_DEFAULT
                    ? {}
                    : { base_branch: event.target.value }),
                })
              }
            >
              <NativeSelectOption value={REPOSITORY_DEFAULT}>
                {branches?.default_branch
                  ? `Repository default (${branches.default_branch})`
                  : "Repository default"}
              </NativeSelectOption>
              {branchOptions.map((branch) => (
                <NativeSelectOption key={branch} value={branch}>
                  {branch}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          ) : (
            <p className="flex min-h-10 items-center text-xs text-muted-foreground">
              The repository’s default remote branch will be used. You can
              override it after creating the project.
            </p>
          )}
        </div>
      )}
      {knownNonGit && (
        <p
          className={
            compact
              ? "basis-full text-xs text-muted-foreground"
              : "text-xs text-muted-foreground"
          }
        >
          Worktrees aren’t available because this directory is not a Git
          repository.
        </p>
      )}
      {error && (
        <p
          role="status"
          className={
            compact
              ? "basis-full text-xs text-destructive"
              : "text-xs text-destructive"
          }
        >
          Could not load remote branches. {error}
        </p>
      )}
    </div>
  );
}

export function WorkspaceSetupBlock({ workspace }: { workspace: Workspace }) {
  if (workspace.status === "fetching" || workspace.status === "creating")
    return (
      <Alert aria-live="polite">
        <LoaderCircle
          className="animate-spin motion-reduce:animate-none"
          aria-hidden="true"
        />
        <AlertTitle>Setting up workspace</AlertTitle>
        <AlertDescription>
          {workspace.status === "fetching"
            ? `Fetching ${workspace.base_branch || "the default remote branch"}…`
            : "Creating an isolated worktree…"}
        </AlertDescription>
      </Alert>
    );
  if (workspace.status === "failed")
    return (
      <Alert variant="destructive">
        <AlertTitle>Workspace setup failed</AlertTitle>
        <AlertDescription className="space-y-2">
          <span className="block [overflow-wrap:anywhere]">
            {workspace.error || "The workspace could not be created."}
          </span>
          <span className="block">
            This chat can’t continue. Start a new chat to choose another
            workspace.
          </span>
        </AlertDescription>
      </Alert>
    );
  return null;
}

async function copyText(text: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Plain-HTTP LAN deployments may expose the Clipboard API but deny it.
    }
  }
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  let copied = false;
  try {
    textarea.select();
    copied = document.execCommand("copy");
  } finally {
    textarea.remove();
  }
  if (!copied) throw new Error("Copy is not available in this browser.");
}

export function WorkspaceIndicator({
  workspace,
  fallbackPath,
  fallbackBranch,
}: {
  workspace: Workspace;
  fallbackPath?: string;
  fallbackBranch?: string;
}) {
  const [copyStatus, setCopyStatus] = useState<"copied" | "failed" | null>(
    null,
  );
  const path =
    workspace.path ||
    (workspace.mode === "current_checkout" ? fallbackPath : undefined);
  // base_branch is only the worktree source. It must never be presented as
  // the live branch of a current checkout.
  const branch =
    workspace.mode === "worktree" ? workspace.branch : fallbackBranch;
  const startFrom =
    workspace.mode === "worktree" && !branch
      ? workspace.base_branch
      : undefined;
  return (
    <div
      role="group"
      aria-label="Workspace location"
      className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground"
    >
      <span className="shrink-0 font-medium text-foreground">
        {workspace.mode === "worktree" ? "Worktree" : "Current checkout"}
      </span>
      {path && (
        <Button
          type="button"
          variant="ghost"
          size="xs"
          className="min-w-0 max-w-full justify-start px-0 font-mono font-normal"
          title={path}
          aria-label={`Copy workspace path: ${path}`}
          onClick={() => {
            setCopyStatus(null);
            void copyText(path)
              .then(() => {
                setCopyStatus("copied");
                window.setTimeout(() => setCopyStatus(null), 2000);
              })
              .catch(() => setCopyStatus("failed"));
          }}
        >
          <span className="hidden truncate md:inline">{path}</span>
          {copyStatus === "copied" ? (
            <Check aria-hidden="true" />
          ) : (
            <Copy aria-hidden="true" />
          )}
        </Button>
      )}
      {branch && (
        <span
          className="flex min-w-0 max-w-full items-center gap-2"
          title={branch}
        >
          <GitBranch className="size-3 shrink-0" aria-hidden="true" />
          <span className="truncate font-mono">{branch}</span>
        </span>
      )}
      {startFrom && (
        <span className="min-w-0 truncate">
          Start from <span className="font-mono">{startFrom}</span>
        </span>
      )}
      <span
        role="status"
        className={copyStatus === "failed" ? "text-destructive" : "sr-only"}
      >
        {copyStatus === "copied"
          ? "Workspace path copied."
          : copyStatus === "failed"
            ? "Could not copy workspace path."
            : ""}
      </span>
    </div>
  );
}
