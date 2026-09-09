import { usePanel } from "@/lib/navigation";
import { lazy, Suspense, useEffect } from "react";
import {
  BrowserRouter,
  Link,
  Route,
  Routes,
  useParams,
} from "react-router-dom";
import { Menu, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Sidebar } from "@/components/sidebar";
import { Dialogs } from "@/components/dialogs";
import { ErrorNotice, Loading } from "@/components/common";
import { control, useControl } from "@/lib/store";

const Chat = lazy(() =>
  import("@/components/chat").then((module) => ({ default: module.Chat })),
);

function Overview() {
  const { projects, loaded } = useControl();
  const { projectId } = useParams();
  const { open } = usePanel();
  const project = projects.find((p) => p.id === projectId);
  const visibleProjects = projectId
    ? projects.filter((p) => p.id === projectId)
    : projects;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-14 shrink-0 items-center gap-4 border-b px-4 md:px-6">
        <Button
          variant="ghost"
          size="icon"
          className="md:hidden"
          aria-label="Open sidebar"
          onClick={() => open("sidebar", "open")}
        >
          <Menu />
        </Button>
        <h1 className="min-w-0 truncate text-sm font-medium">
          {projectId ? project?.name || "Project not found" : "Projects"}
        </h1>
      </header>
      {!loaded ? (
        <Loading>Loading projects…</Loading>
      ) : (
        <div className="flex-1 overflow-y-auto p-4 md:p-6">
          <div className="mx-auto max-w-chat space-y-6">
            <div className="flex flex-wrap items-center justify-between gap-4">
              <p className="text-sm text-muted-foreground">
                Server directories available for chats.
              </p>
              <Button
                variant="outline"
                onClick={() => open("dialog", "new-project")}
              >
                <Plus />
                New project
              </Button>
            </div>
            <div className="space-y-2">
              {visibleProjects.map((p) => (
                <Button
                  key={p.id}
                  variant="outline"
                  className="h-auto w-full justify-start rounded-xl p-inset text-left"
                  render={
                    <Link
                      to={`/projects/${encodeURIComponent(p.id)}?panel=settings`}
                    />
                  }
                >
                  <span className="min-w-0">
                    <span className="block truncate">{p.name}</span>
                    <span
                      className="block truncate font-mono text-xs font-normal text-muted-foreground"
                      title={p.root}
                    >
                      {p.root}
                    </span>
                  </span>
                </Button>
              ))}
            </div>
            {!visibleProjects.length && (
              <p className="py-8 text-sm text-muted-foreground">
                {projectId
                  ? "This project is not available."
                  : "No projects yet. Create a project to start chatting."}
              </p>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
function Layout() {
  const { agentId } = useParams();
  const { status, error, loaded } = useControl();
  useEffect(() => {
    control.select(agentId);
    return () => control.select(undefined);
  }, [agentId]);
  return (
    <div className="flex h-dvh overflow-hidden">
      <Sidebar />
      <main className="flex min-w-0 flex-1 flex-col">
        {(status === "offline" || error) && (
          <div className="shrink-0 border-b p-4">
            <div className="flex items-center justify-between gap-4 text-sm">
              <span role="status">
                {loaded
                  ? "Connection interrupted. Reconnecting safely from the last processed event."
                  : "Could not connect to the server."}
              </span>
              <Button
                variant="outline"
                size="xs"
                onClick={() => void control.start()}
              >
                Reconnect
              </Button>
            </div>
            {error && (
              <div className="mt-2">
                <ErrorNotice error={error} />
              </div>
            )}
          </div>
        )}
        <Suspense fallback={<Loading>Opening chat…</Loading>}>
          {agentId ? <Chat key={agentId} /> : <Overview />}
        </Suspense>
        <Dialogs />
      </main>
    </div>
  );
}
export default function App() {
  useEffect(() => {
    void control.start();
    return control.stop;
  }, []);
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<Layout />} />
        <Route path="/agents/:agentId" element={<Layout />} />
        <Route path="/projects/:projectId" element={<Layout />} />
        <Route
          path="*"
          element={
            <div className="p-12">
              <h1 className="text-xl">Page not found</h1>
              <Link className="text-primary" to="/">
                Back to workspace
              </Link>
            </div>
          }
        />
      </Routes>
    </BrowserRouter>
  );
}
