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

function useProjectBranches(projectId?: string, root?: string) {
  const path = projectId
    ? `/v1/projects/${encodeURIComponent(projectId)}/branches`
    : root?.trim()
      ? `/v1/projects/branches?${new URLSearchParams({ root: root.trim() })}`
      : undefined;
  const [state, setState] = useState<{
    path?: string;
    branches: ProjectBranches | null;
    error: string | null;
  }>({ path, branches: null, error: null });
  useEffect(() => {
    let active = true;
    if (!path) return;
    const timer = setTimeout(
      () => {
        void api<ProjectBranches>(path)
          .then((branches) => {
            if (active) setState({ path, branches, error: null });
          })
          .catch((error) => {
            if (active)
              setState({ path, branches: null, error: String(error) });
          });
      },
      projectId ? 0 : 300,
    );
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [path, projectId]);
  return state.path === path
    ? { ...state, loading: !!path && !state.branches && !state.error }
    : { branches: null, error: null, loading: !!path };
}

export function WorkspaceFields({
  projectId,
  root,
  value,
  onChange,
  disabled = false,
  compact = false,
  gitBranch,
  prNumber,
}: {
  projectId?: string;
  root?: string;
  value: WorkspaceSelection;
  onChange: (value: WorkspaceSelection) => void;
  disabled?: boolean;
  compact?: boolean;
  gitBranch?: string;
  prNumber?: number;
}) {
  const locationId = useId();
  const branchId = useId();
  const { branches, error, loading } = useProjectBranches(projectId, root);
  const knownNonGit = branches?.is_git === false;
  const availableBranches = branches?.branches || [];
  const defaultBranch = branches?.default_branch || availableBranches[0];
  const initialBranch = availableBranches.includes(value.base_branch || "")
    ? value.base_branch
    : defaultBranch;
  useEffect(() => {
    if (
      !projectId &&
      value.mode === "worktree" &&
      !value.base_branch &&
      defaultBranch
    )
      onChange({ mode: "worktree", base_branch: defaultBranch });
  }, [projectId, value.mode, value.base_branch, defaultBranch, onChange]);
  const noRemoteBranches = !!branches?.is_git && !availableBranches.length;
  const branchValue = value.base_branch || REPOSITORY_DEFAULT;
  const branchOptions = [...(branches?.branches || [])];
  if (value.base_branch && !branchOptions.includes(value.base_branch))
    branchOptions.unshift(value.base_branch);
  const branchItems = [
    {
      value: REPOSITORY_DEFAULT,
      label: loading
        ? "Loading branches…"
        : compact
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
                  disabled={
                    (!projectId && !root?.trim()) ||
                    knownNonGit ||
                    noRemoteBranches ||
                    !!error
                  }
                >
                  Worktree
                </SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      </div>
      {compact && gitBranch && (
        <WorkspaceBranch branch={gitBranch} prNumber={prNumber} />
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
          {projectId || root?.trim() ? (
            <Select
              items={branchItems}
              value={branchValue}
              disabled={
                disabled ||
                loading ||
                knownNonGit ||
                noRemoteBranches ||
                !!error
              }
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
              Enter a server directory to choose a starting branch.
            </p>
          )}
        </div>
      )}
      {loading && !compact && (
        <p
          role="status"
          className="col-span-full text-xs text-muted-foreground"
        >
          Loading remote branches…
        </p>
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

export function PullRequestNumber({ number }: { number: number }) {
  return (
    <span
      className="shrink-0 font-mono text-xs"
      role="group"
      title={`GitHub pull request #${number}`}
      aria-label={`Pull request #${number}`}
    >
      #{number}
    </span>
  );
}

export function WorkspaceBranch({
  branch,
  prNumber,
}: {
  branch: string;
  prNumber?: number;
}) {
  return (
    <div className="flex min-w-16 max-w-full flex-1 items-center gap-2">
      <FooterSeparator />
      {prNumber && <PullRequestNumber number={prNumber} />}
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
