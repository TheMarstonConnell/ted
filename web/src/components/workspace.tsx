import { useEffect, useId, useState } from "react";
import { Folder, GitBranch, LoaderCircle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { cn } from "@/lib/utils";
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
  gitBranch,
}: {
  projectId?: string;
  value: WorkspaceSelection;
  onChange: (value: WorkspaceSelection) => void;
  disabled?: boolean;
  compact?: boolean;
  gitBranch?: string;
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
        compact
          ? "flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-0 md:gap-x-4"
          : "space-y-4"
      }
      role="group"
      aria-label="Workspace settings"
    >
      <div className={compact ? "min-w-0 max-w-full" : "grid min-w-0 gap-2"}>
        <Label htmlFor={locationId} className={compact ? "sr-only" : undefined}>
          Workspace
        </Label>
        <div className="flex min-w-0 items-center gap-2 text-muted-foreground">
          {compact && value.mode === "current_checkout" && (
            <Folder className="size-3 shrink-0" aria-hidden="true" />
          )}
          <NativeSelect
            id={locationId}
            size={compact ? "sm" : "default"}
            variant={compact ? "plain" : "default"}
            className={compact ? "max-w-full" : "w-full"}
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
              Local
            </NativeSelectOption>
            <NativeSelectOption value="worktree" disabled={knownNonGit}>
              Worktree
            </NativeSelectOption>
          </NativeSelect>
        </div>
      </div>
      {compact && value.mode === "current_checkout" && gitBranch && (
        <WorkspaceBranch branch={gitBranch} />
      )}
      {value.mode === "worktree" && (
        <div
          className={
            compact
              ? "flex min-w-16 max-w-full flex-1 items-center gap-2 md:gap-4"
              : "grid min-w-0 gap-2"
          }
        >
          {compact && <FooterSeparator />}
          <Label htmlFor={branchId} className={compact ? "sr-only" : undefined}>
            Start from
          </Label>
          {projectId ? (
            <NativeSelect
              id={branchId}
              size={compact ? "sm" : "default"}
              variant={compact ? "plain" : "default"}
              className={
                compact ? "min-w-0 max-w-full flex-1" : "w-full font-mono"
              }
              title={`Start from: ${value.base_branch || branches?.default_branch || "remote branch"}`}
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
                {compact
                  ? branches?.default_branch || "Start from…"
                  : branches?.default_branch
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

// Once the first message is accepted, this is metadata, not a disabled control.
// Keep the actual path available as a tooltip without adding directory chrome.
export function WorkspaceIndicator({
  workspace,
  fallbackPath,
}: {
  workspace: Workspace;
  fallbackPath?: string;
}) {
  return (
    <span
      role="group"
      aria-label="Workspace location"
      className="inline-flex min-w-0 items-center gap-2 font-mono text-xs text-muted-foreground"
      title={
        workspace.path ||
        (workspace.mode === "current_checkout" ? fallbackPath : undefined)
      }
    >
      {workspace.mode === "current_checkout" && (
        <Folder className="size-3 shrink-0" aria-hidden="true" />
      )}
      {workspace.mode === "worktree" ? "Worktree" : "Local"}
    </span>
  );
}

// Decorative dividers do not add tab stops or screen-reader announcements.
export function FooterSeparator({ className }: { className?: string }) {
  return (
    <Separator
      orientation="vertical"
      aria-hidden="true"
      className={cn("h-4 data-vertical:self-center", className)}
    />
  );
}

export function WorkspaceBranch({ branch }: { branch: string }) {
  return (
    <div className="flex min-w-16 max-w-full flex-1 items-center gap-2 md:gap-4">
      <FooterSeparator />
      <span
        role="group"
        aria-label="Git branch"
        title={branch}
        className="inline-flex min-w-0 items-center gap-2 font-mono text-xs text-muted-foreground"
      >
        <GitBranch className="size-3 shrink-0" aria-hidden="true" />
        <span className="truncate">{branch}</span>
      </span>
    </div>
  );
}
