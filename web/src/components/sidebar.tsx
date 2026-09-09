import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Archive,
  ArchiveRestore,
  ChevronRight,
  GitBranch,
  FolderPlus,
  Plus,
  Settings2,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import {
  Sheet,
  SheetContent,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import { groupAgents, agentTitle, type Agent } from "@/lib/api";
import { control, useControl } from "@/lib/store";
import { usePanel } from "@/lib/navigation";
import { ErrorNotice } from "@/components/common";

export function AgentLink({ agent }: { agent: Agent }) {
  const { agentId } = useParams();
  const { projects, status } = useControl();
  const project = projects.find((p) => p.id === agent.project_id);
  const branch = project?.git_branch;
  const title = agentTitle(agent);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pending = useRef(false);
  const toggleSettled = async () => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError(null);
    try {
      await control.settle(agent.id, !agent.settled);
    } catch (error) {
      setError(String(error));
    } finally {
      pending.current = false;
      setBusy(false);
    }
  };
  return (
    <div data-agent-id={agent.id} className="space-y-1">
      <div className="group/agent flex items-center gap-1 transition-[gap] duration-150 [@media(hover:hover)_and_(pointer:fine)]:gap-0 hover:gap-1 focus-within:gap-1">
        <Button
          variant={agentId === agent.id ? "secondary" : "ghost"}
          className="h-auto min-w-0 flex-1 justify-start py-2 font-normal"
          render={
            <Link
              to={`/agents/${encodeURIComponent(agent.id)}`}
              title={title}
              aria-current={agentId === agent.id ? "page" : undefined}
            />
          }
        >
          <span className="min-w-0 flex-1 text-left">
            <span className="flex items-center gap-2">
              <span className="min-w-0 flex-1 truncate">{title}</span>
              {!agent.settled && (agent.held || agent.state !== "idle") && (
                <span className="shrink-0 text-xs text-muted-foreground">
                  {agent.held
                    ? "Held"
                    : agent.state === "stopping"
                      ? "Stopping"
                      : "Running"}
                </span>
              )}
            </span>
            <span
              className="mt-1 flex min-w-0 items-center gap-1 text-xs text-muted-foreground"
              title={branch || undefined}
            >
              {branch && (
                <GitBranch className="size-3 shrink-0" aria-hidden="true" />
              )}
              <span className="truncate">
                {branch || (project ? "Branch unavailable" : "No project")}
              </span>
            </span>
            <span className="sr-only">
              {agent.settled ? "Settled" : agent.state}
            </span>
          </span>
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="overflow-hidden border-x-0 [@media(hover:hover)_and_(pointer:fine)]:w-0 group-hover/agent:w-8 group-focus-within/agent:w-8 [@media(hover:hover)_and_(pointer:fine)]:opacity-0 [@media(hover:hover)_and_(pointer:fine)]:disabled:opacity-0 group-hover/agent:opacity-100 group-hover/agent:disabled:opacity-50 group-focus-within/agent:opacity-100 group-focus-within/agent:disabled:opacity-50"
          aria-label={`${agent.settled ? "Restore" : "Settle"} chat: ${title}`}
          title={agent.settled ? "Restore chat" : "Settle chat"}
          disabled={busy || status !== "live"}
          onClick={() => void toggleSettled()}
        >
          {agent.settled ? <ArchiveRestore /> : <Archive />}
        </Button>
      </div>
      <ErrorNotice error={error} />
    </div>
  );
}
function SidebarContent() {
  const { agents, projects, loaded } = useControl();
  const { params, open, close, openProjectSettings } = usePanel();
  const groups = groupAgents(Object.values(agents), projects);
  return (
    <div className="flex h-full min-h-0 flex-col bg-sidebar">
      <div className="flex h-14 shrink-0 items-center justify-between px-3">
        <Button variant="ghost" render={<Link to="/" />}>
          Ted
        </Button>
        {params.has("sidebar") && (
          <Button
            variant="ghost"
            size="icon"
            aria-label="Close sidebar"
            onClick={() => close("sidebar")}
          >
            <X />
          </Button>
        )}
      </div>
      <div className="px-3 pb-4">
        <div
          role="group"
          aria-label="Create chat or project"
          className="flex w-full gap-2"
        >
          <Button
            className="min-w-0 flex-1 justify-start"
            onClick={() => open("dialog", "new-agent")}
          >
            <Plus /> New chat
          </Button>
          <Button
            variant="outline"
            size="icon"
            aria-label="New project"
            title="New project"
            onClick={() => open("dialog", "new-project")}
          >
            <FolderPlus />
          </Button>
        </div>
      </div>
      <nav
        aria-label="Agents"
        className="min-h-0 flex-1 space-y-4 overflow-y-auto px-3 pb-4"
      >
        {groups.misc.length > 0 && (
          <div className="space-y-1">
            <p className="px-2 py-2 text-xs text-muted-foreground">Misc</p>
            {groups.misc.map((a) => (
              <AgentLink key={a.id} agent={a} />
            ))}
          </div>
        )}
        {groups.projects.map(({ project, agents: projectAgents }) => (
          <Collapsible key={project.id} defaultOpen>
            <div className="flex items-center gap-1">
              <CollapsibleTrigger
                render={
                  <Button
                    variant="ghost"
                    className="group min-w-0 flex-1 justify-start"
                  />
                }
              >
                <ChevronRight className="size-4 group-data-panel-open:rotate-90" />
                <span className="truncate">{project.name}</span>
              </CollapsibleTrigger>
              <Button
                variant="ghost"
                size="icon"
                aria-label={`Settings for ${project.name}`}
                onClick={() => openProjectSettings(project.id)}
              >
                <Settings2 />
              </Button>
            </div>
            <CollapsibleContent className="space-y-1 pt-1">
              {projectAgents.length ? (
                projectAgents.map((a) => <AgentLink key={a.id} agent={a} />)
              ) : (
                <p className="px-2 py-2 text-xs text-muted-foreground">
                  No active chats
                </p>
              )}
            </CollapsibleContent>
          </Collapsible>
        ))}
        {loaded && !projects.length && (
          <p className="px-2 text-sm text-muted-foreground">
            Create a project to start your first chat.
          </p>
        )}
        <Collapsible>
          <CollapsibleTrigger
            render={
              <Button variant="ghost" className="group w-full justify-start" />
            }
          >
            <ChevronRight className="size-4 group-data-panel-open:rotate-90" />
            Settled chats
          </CollapsibleTrigger>
          <CollapsibleContent className="space-y-1 pt-1">
            {groups.settled.map((a) => (
              <AgentLink key={a.id} agent={a} />
            ))}
            {!groups.settled.length && (
              <p className="px-2 py-2 text-xs text-muted-foreground">
                No settled chats
              </p>
            )}
          </CollapsibleContent>
        </Collapsible>
      </nav>
    </div>
  );
}
export function Sidebar() {
  const { params, close } = usePanel();
  // Unmount the drawer on navigation/overlay hand-off. Keeping a closed root
  // while re-keying its portalled content can leave a modal focus/interaction
  // lock behind. There must be only one active modal owner at a time.
  // Entry keyframes animate this initially-open mount without delaying cleanup.
  const drawerOpen =
    params.get("sidebar") === "open" &&
    !params.has("panel") &&
    !params.has("dialog");
  return (
    <>
      <aside className="hidden w-64 shrink-0 border-r md:block">
        <SidebarContent />
      </aside>
      {drawerOpen && (
        <Sheet
          open
          onOpenChange={(open) => {
            if (!open) close("sidebar");
          }}
        >
          <SheetContent
            side="bottom"
            overlayClassName="motion-safe:animate-in motion-safe:fade-in-0 motion-safe:duration-200"
            className="overflow-hidden rounded-t-xl bg-sidebar p-0 pb-[env(safe-area-inset-bottom)] data-[side=bottom]:h-[80dvh] data-starting-style:opacity-100 data-[side=bottom]:data-starting-style:translate-y-0 motion-safe:animate-in motion-safe:fade-in-0 motion-safe:slide-in-from-bottom-4 motion-safe:duration-200 motion-safe:ease-out"
            showCloseButton={false}
          >
            <SheetTitle className="sr-only">Workspace</SheetTitle>
            <SheetDescription className="sr-only">
              Projects and agent chats
            </SheetDescription>
            <SidebarContent />
          </SheetContent>
        </Sheet>
      )}
    </>
  );
}
