import { useEffect, useId, useState } from "react";
import { Folder, GitBranch, LoaderCircle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { cn } from "@/lib/utils";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectGroup,
  SelectItem,
} from "@/components/ui/select";
import {
  api,
  type ProjectBranches,
  type Workspace,
  type WorkspaceSelection,
} from "@/lib/api";

const REPOSITORY_DEFAULT = "__repository_default__";
const COMPACT_TRIGGER_CLASS =
  "min-w-0 max-w-full border-transparent bg-transparent px-2 font-mono text-xs dark:bg-transparent dark:hover:bg-accent";
const FIELD_CLASS = "grid min-w-0 max-w-full grid-cols-[minmax(0,1fr)] gap-2";

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
  const availableBranches = branches?.branches || [];
  const defaultBranch = branches?.default_branch || availableBranches[0];
  const initialBranch = availableBranches.includes(value.base_branch || "")
    ? value.base_branch
    : defaultBranch;
  const noRemoteBranches = !!branches?.is_git && !availableBranches.length;
  const branchValue = value.base_branch || REPOSITORY_DEFAULT;
  const branchOptions = [...(branches?.branches || [])];
  if (value.base_branch && !branchOptions.includes(value.base_branch))
    branchOptions.unshift(value.base_branch);
  const branchItems = [
    {
      value: REPOSITORY_DEFAULT,
      label: compact
        ? defaultBranch || "Start from…"
        : defaultBranch
          ? `Repository default (${defaultBranch})`
          : "Repository default",
    },
    ...branchOptions.map((branch) => ({ value: branch, label: branch })),
  ];

  return (
    <div
      className={
        compact
          ? "flex min-w-0 flex-1 flex-wrap items-center gap-2"
          : "grid min-w-0 gap-4 sm:grid-cols-2"
      }
      role="group"
      aria-label="Workspace settings"
    >
      <div className={compact ? "min-w-0 max-w-full" : FIELD_CLASS}>
        <Label htmlFor={locationId} className={compact ? "sr-only" : undefined}>
          Workspace
        </Label>
        <div className="flex min-w-0 items-center gap-2 text-muted-foreground">
          {compact && value.mode === "current_checkout" && (
            <Folder className="size-3 shrink-0" aria-hidden="true" />
          )}
          <Select
            items={[
              { value: "current_checkout", label: "Local" },
              { value: "worktree", label: "Worktree" },
            ]}
            value={value.mode}
            disabled={disabled || loading}
            onValueChange={(mode) => {
              if (mode !== "worktree" && mode !== "current_checkout") return;
              onChange(
                mode === "worktree"
                  ? {
                      mode,
                      ...(initialBranch ? { base_branch: initialBranch } : {}),
                    }
                  : { mode },
              );
            }}
          >
            <SelectTrigger
              id={locationId}
              size={compact ? "sm" : "default"}
              className={
                compact ? COMPACT_TRIGGER_CLASS : "w-full text-foreground"
              }
            >
              <SelectValue className="min-w-0 truncate" />
            </SelectTrigger>
            <SelectContent
              side={compact ? "top" : "bottom"}
              align="start"
              alignItemWithTrigger={false}
            >
              <SelectGroup>
                <SelectItem value="current_checkout">Local</SelectItem>
                <SelectItem
                  value="worktree"
                  disabled={knownNonGit || noRemoteBranches || !!error}
                >
                  Worktree
                </SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      </div>
      {compact && value.mode === "current_checkout" && gitBranch && (
        <WorkspaceBranch branch={gitBranch} />
      )}
      {value.mode === "worktree" && (
        <div
          className={
            compact
              ? "flex min-w-16 max-w-full flex-1 items-center gap-2"
              : FIELD_CLASS
          }
        >
          {compact && <FooterSeparator />}
          <Label htmlFor={branchId} className={compact ? "sr-only" : undefined}>
            Start from
          </Label>
          {projectId ? (
            <Select
              items={branchItems}
              value={branchValue}
              disabled={disabled || loading || knownNonGit || !!error}
              onValueChange={(branch) =>
                branch &&
                onChange({
                  mode: "worktree",
                  ...(branch === REPOSITORY_DEFAULT
                    ? defaultBranch
                      ? { base_branch: defaultBranch }
                      : {}
                    : { base_branch: branch }),
                })
              }
            >
              <SelectTrigger
                id={branchId}
                size={compact ? "sm" : "default"}
                className={compact ? COMPACT_TRIGGER_CLASS : "w-full font-mono"}
                title={`Start from: ${value.base_branch || defaultBranch || "remote branch"}`}
              >
                <SelectValue className="min-w-0 truncate" />
              </SelectTrigger>
              <SelectContent
                side={compact ? "top" : "bottom"}
                align="start"
                alignItemWithTrigger={false}
                className="w-max max-w-[calc(100vw-2rem)]"
              >
                <SelectGroup>
                  {branchItems.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
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
              : "col-span-full text-xs text-muted-foreground"
          }
        >
          Worktrees aren’t available because this directory is not a Git
          repository.
        </p>
      )}
      {noRemoteBranches && (
        <p
          role="status"
          className="col-span-full basis-full text-xs text-muted-foreground"
        >
          No remote branches are available. Fetch branches from a Git remote,
          then reopen this chat or settings to choose a worktree base.
        </p>
      )}
      {error && (
        <p
          role="status"
          className={
            compact
              ? "basis-full text-xs text-destructive"
              : "col-span-full text-xs text-destructive"
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
    <div className="flex min-w-16 max-w-full flex-1 items-center gap-2">
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
