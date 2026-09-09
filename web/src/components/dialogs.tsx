import { usePanel } from "@/lib/navigation";
import { useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Plus, Trash2 } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api, projectName, requestKey, type Project } from "@/lib/api";
import { control, useControl } from "@/lib/store";
import { ErrorNotice, ModelFields } from "./common";

function ProjectPicker() {
  const { projects, loaded } = useControl();
  const { open } = usePanel();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [receipt, setReceipt] = useState<{ project: string; key: string }>();
  async function create(project: string) {
    if (busy) return;
    setBusy(true);
    setError(null);
    const request =
      receipt?.project === project ? receipt : { project, key: requestKey() };
    setReceipt(request);
    try {
      const agent = await control.createAgent(project, request.key);
      navigate(`/agents/${encodeURIComponent(agent.id)}`);
    } catch (error) {
      setError(String(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>New chat</DialogTitle>
        <DialogDescription>
          Choose a project to use for this chat.
        </DialogDescription>
      </DialogHeader>
      <ErrorNotice error={error} />
      <div className="max-h-80 space-y-2 overflow-y-auto">
        {projects.map((p) => (
          <Button
            variant="outline"
            key={p.id}
            disabled={busy}
            onClick={() => void create(p.id)}
            className="h-auto w-full justify-start px-4 py-4 text-left"
          >
            <span className="min-w-0 flex-1">
              <span className="block truncate text-sm font-medium">
                {p.name}
              </span>
              <span className="block truncate font-mono text-xs font-normal text-muted-foreground">
                {p.root}
              </span>
            </span>
          </Button>
        ))}
        {loaded && !projects.length && (
          <p className="py-6 text-center text-sm text-muted-foreground">
            No projects yet. Create one to start chatting.
          </p>
        )}
      </div>
      <Button
        variant="outline"
        disabled={busy}
        onClick={() => open("dialog", "new-project")}
      >
        <Plus />
        Create a project
      </Button>
      {busy && (
        <p role="status" className="text-xs text-muted-foreground">
          Creating your chat…
        </p>
      )}
    </>
  );
}
function ProjectForm() {
  const { projectId } = useParams();
  const { projects, models } = useControl();
  const { close, params } = usePanel();
  const project =
    params.get("dialog") === "new-project"
      ? undefined
      : projects.find(
          (p) =>
            p.id ===
            (params.get("panel") === "project-settings"
              ? params.get("project")
              : projectId),
        );
  const navigate = useNavigate();
  const [root, setRoot] = useState(project?.root || "");
  const [model, setModel] = useState(
    project?.defaults.model || models[0]?.id || "",
  );
  const [effort, setEffort] = useState(
    project?.defaults.effort ?? models[0]?.default_effort ?? "",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const name = projectName(root);
  async function save(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await api<Project>(
        project
          ? `/v1/projects/${encodeURIComponent(project.id)}`
          : "/v1/projects",
        project ? "PATCH" : "POST",
        project
          ? { defaults: { model, effort } }
          : { name, root: root.trim(), defaults: { model, effort } },
      );
      await control.refreshProjects();
      if (project) close("panel");
      else navigate("/?dialog=new-agent");
    } catch (error) {
      setError(String(error));
    } finally {
      setBusy(false);
    }
  }
  async function remove() {
    setBusy(true);
    setError(null);
    try {
      await api(`/v1/projects/${encodeURIComponent(project!.id)}`, "DELETE");
      await control.refreshProjects();
      if (projectId === project!.id) navigate("/");
      else close("panel");
    } catch (error) {
      setError(String(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {project ? `${project.name} defaults` : "Create a project"}
        </DialogTitle>
        <DialogDescription>
          {project
            ? "Defaults apply to new agents only. Existing agents keep their settings."
            : "Connect a directory on the server. Its folder name becomes the project name."}
        </DialogDescription>
      </DialogHeader>
      <form onSubmit={(event) => void save(event)} className="space-y-6">
        <ErrorNotice error={error} />
        <div className="grid gap-2">
          <Label htmlFor="server-directory">Server directory</Label>
          <Input
            id="server-directory"
            className="font-mono"
            autoFocus={!project}
            required
            maxLength={4096}
            placeholder="/path/to/your/project"
            value={root}
            readOnly={!!project}
            onChange={(event) => setRoot(event.target.value)}
          />
          <p className="text-xs text-muted-foreground">
            Project name:{" "}
            <strong className="text-foreground">
              {project?.name || name || "—"}
            </strong>
            {!project && (
              <span className="block">
                The directory must already exist on the server.
              </span>
            )}
          </p>
          {project?.git_branch && (
            <p className="text-xs text-muted-foreground">
              Git branch:{" "}
              <span className="font-mono">{project.git_branch}</span>
            </p>
          )}
        </div>
        <ModelFields
          model={model}
          effort={effort}
          onChange={(m, e) => {
            setModel(m);
            setEffort(e);
          }}
        />
        <div className="flex justify-end gap-2">
          <Button type="submit" disabled={busy || !model || !name}>
            {busy ? "Saving…" : project ? "Save defaults" : "Create project"}
          </Button>
        </div>
      </form>
      {project && (
        <div className="border-t border-border pt-4">
          {confirmDelete ? (
            <div className="space-y-4">
              <p className="text-xs text-muted-foreground">
                Delete this project? Only empty projects can be deleted,
                including settled chats. The directory itself will not be
                removed.
              </p>
              <div className="flex flex-wrap gap-2">
                <Button
                  variant="destructive"
                  disabled={busy}
                  onClick={() => void remove()}
                >
                  Confirm delete
                </Button>
                <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
                  Keep project
                </Button>
              </div>
            </div>
          ) : (
            <Button
              variant="ghost"
              className="text-destructive"
              onClick={() => setConfirmDelete(true)}
            >
              <Trash2 />
              Delete project
            </Button>
          )}
        </div>
      )}
    </>
  );
}
export function Dialogs() {
  const { agentId, projectId } = useParams();
  const { projects, loaded } = useControl();
  const { params, close } = usePanel();
  const dialog = params.get("dialog");
  const settingsProjectId =
    params.get("panel") === "project-settings"
      ? params.get("project")
      : projectId;
  const isProjectSettings =
    ["settings", "project-settings"].includes(params.get("panel") || "") &&
    !!settingsProjectId &&
    projects.some((p) => p.id === settingsProjectId);
  const show =
    dialog === "new-agent" || dialog === "new-project" || isProjectSettings;
  if (!show || !loaded) return null;
  return (
    <Dialog
      key={`${dialog}-${agentId}-${settingsProjectId}`}
      open={show}
      onOpenChange={(open) => {
        if (!open) close(dialog ? "dialog" : "panel");
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        {dialog === "new-agent" ? (
          <ProjectPicker />
        ) : dialog === "new-project" ? (
          <ProjectForm key="new" />
        ) : isProjectSettings ? (
          <ProjectForm key={settingsProjectId} />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
