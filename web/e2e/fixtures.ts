import { expect, type Page } from "@playwright/test";

// Deterministic browser tests: no provider credentials, paid turns, or writes to
// real projects. Real same-origin Go/Vite smoke testing is documented separately.
export async function workspace(page: Page) {
  let project = {
    id: "p",
    name: "harness",
    root: "/srv/harness",
    git_branch: "main",
    defaults: { model: "test/model", effort: "medium" },
    workspace_defaults: { mode: "current_checkout" },
  };
  let projectBranches = {
    is_git: true,
    branches: ["origin/main", "origin/release", "upstream/trunk"],
    default_branch: "origin/main",
  };
  const agents: Record<string, any> = {};
  const events: Record<string, any[]> = {};
  const sockets: any[] = [];
  function inventory(id: string) {
    sockets.forEach((ws) =>
      ws.send(JSON.stringify({ type: "inventory", agent: agents[id] })),
    );
  }
  function emit(id: string, type: string, data: unknown) {
    const event = {
      agent_id: id,
      cursor: ++agents[id].cursor,
      type,
      data,
      created_at: new Date().toISOString(),
    };
    events[id].push(event);
    inventory(id);
    sockets.forEach((ws) => ws.send(JSON.stringify({ type: "event", event })));
  }
  await page.routeWebSocket("**/v1/ws", (ws) => {
    sockets.push(ws);
    ws.onMessage((raw) => {
      const request = JSON.parse(String(raw));
      ws.send(
        JSON.stringify({ type: "subscribed", request_id: request.request_id }),
      );
      Object.values(agents).forEach((agent) => {
        ws.send(JSON.stringify({ type: "inventory", agent }));
        events[agent.id]
          .filter((e) => e.cursor > (request.cursors[agent.id] || 0))
          .forEach((event) =>
            ws.send(JSON.stringify({ type: "event", event })),
          );
      });
    });
  });
  await page.route("**/v1/**", async (route) => {
    const request = route.request(),
      url = new URL(request.url()),
      method = request.method();
    const body = request.postDataJSON();
    let result: unknown;
    if (url.pathname === "/v1/projects") {
      if (method === "POST") project = { ...project, ...body };
      result = method === "POST" ? project : [project];
    } else if (url.pathname === "/v1/projects/p/branches") {
      result = projectBranches;
    } else if (url.pathname === "/v1/projects/p") {
      if (method === "PATCH") project = { ...project, ...body };
      result = project;
    } else if (url.pathname === "/v1/models")
      result = [
        {
          id: "test/model",
          name: "Test model",
          provider: "test",
          context_window: 100000,
          efforts: ["low", "medium", "high"],
          default_effort: "medium",
        },
      ];
    else if (url.pathname === "/v1/agents") {
      if (method === "POST") {
        expect(body).toEqual({ project_id: "p" });
        const id = `a${Object.keys(agents).length + 1}`;
        agents[id] = {
          id,
          project_id: "p",
          title: "",
          settings: project.defaults,
          settled: false,
          held: false,
          state: "idle",
          cursor: 0,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
          workspace: {
            ...(body.workspace || project.workspace_defaults),
            locked: false,
            status: "draft",
          },
        };
        events[id] = [];
        inventory(id);
        result = agents[id];
      } else result = Object.values(agents);
    } else {
      const [, , , id, operation] = url.pathname.split("/");
      if (method === "PATCH" && !operation) {
        agents[id].settled = body.settled;
        inventory(id);
      }
      if (operation === "workspace" && method === "PATCH") {
        agents[id].workspace = {
          ...body,
          locked: false,
          status: "draft",
        };
        inventory(id);
        result = agents[id];
      }
      if (operation === "messages" && method === "DELETE") {
        const messageId = decodeURIComponent(url.pathname.split("/")[5]);
        const message = events[id].findLast(
          (event) =>
            (event.type.startsWith("message.") ||
              event.type.startsWith("turn.")) &&
            event.data.id === messageId,
        )?.data;
        if (!message || !["pending", "cancelled"].includes(message.status)) {
          await route.fulfill({
            status: message ? 409 : 404,
            json: {
              error: { message: "Only pending messages can be removed" },
            },
          });
          return;
        }
        if (message.status === "pending")
          emit(id, "message.cancelled", { ...message, status: "cancelled" });
        await route.fulfill({ status: 204 });
        return;
      }
      if (operation === "messages" && method === "POST") {
        expect(agents[id].settled).toBe(false);
        const selected = agents[id].workspace;
        agents[id].workspace = {
          ...selected,
          locked: true,
          status: "ready",
          path:
            selected.mode === "worktree"
              ? `/var/lib/ted/worktrees/${id}`
              : project.root,
          branch:
            selected.mode === "worktree"
              ? selected.base_branch || "origin/main"
              : project.git_branch,
          ...(selected.mode === "worktree"
            ? { base_commit: "0123456789abcdef" }
            : {}),
        };
        inventory(id);
        expect(request.headers()["idempotency-key"]).toBeTruthy();
        const queued = {
          id: `m${events[id].length}`,
          text: body.text,
          status: "pending",
          created_at: new Date().toISOString(),
        };
        emit(id, "message.queued", queued);
        emit(id, "turn.started", { ...queued, status: "running" });
        emit(id, "output", {
          Content: "**Ready to build.**\n\n```ts\nconst connected = true\n```",
          ResponseType: "agent",
          ToolCallID: "",
          ToolName: "",
          FullToolOutput: "",
        });
        emit(id, "turn.completed", { ...queued, status: "completed" });
        result = queued;
      } else if (operation === "settings" && method === "PATCH") {
        agents[id].settings = { ...agents[id].settings, ...body };
        inventory(id);
        result = agents[id];
      } else result = agents[id];
    }
    await route.fulfill({
      status:
        method === "POST" && !url.pathname.endsWith("messages") ? 201 : 200,
      json: result,
    });
  });
  return {
    agents,
    events,
    emit,
    setWorkspace(id: string, workspace: Record<string, unknown>) {
      agents[id].workspace = workspace;
      inventory(id);
    },
    setProjectWorkspaceDefaults(value: { mode?: string }) {
      project.workspace_defaults = value as typeof project.workspace_defaults;
    },
    setProjectBranches(value: typeof projectBranches) {
      projectBranches = value;
    },
  };
}

// Project defaults remain accessible from the sidebar on desktop and mobile.
export async function openProjectDefaults(page: Page) {
  const settings = page.getByRole("button", {
    name: "Settings for harness",
    exact: true,
  });
  const project = page.getByRole("button", { name: "harness", exact: true });
  if (!(await project.isVisible())) {
    await page
      .getByRole("button", { name: "Open sidebar", exact: true })
      .click();
  }
  // Focusing the project row reveals its action on desktop without relying on hover.
  await project.focus();
  await settings.click();
  await expect(
    page.getByRole("dialog", { name: "harness defaults", exact: true }),
  ).toBeVisible();
}
