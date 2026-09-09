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
    } else if (url.pathname === "/v1/projects/p") result = project;
    else if (url.pathname === "/v1/models")
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
  return { agents, events, emit };
}

// Open the supported settings deep link without resetting in-memory drafts.
// Context is read-only; model/effort editing remains in the composer toolbar.
export async function openAgentSettings(page: Page) {
  await page.evaluate(() => {
    const url = new URL(location.href);
    url.searchParams.set("panel", "settings");
    history.pushState(null, "", url);
    dispatchEvent(new PopStateEvent("popstate"));
  });
}
