import { afterEach, describe, expect, it, vi } from "vitest";
import { ControlPlane, isUnread, reduceEvent, type State } from "./store";
import {
  groupAgents,
  branchPullRequest,
  projectName,
  type Agent,
  type Event,
  type Project,
  type Schema,
} from "./api";

const agent = (id = "a", overrides: Partial<Agent> = {}): Agent => ({
  id,
  project_id: "p",
  title: "",
  settings: { model: "test/model", effort: "high" },
  settled: false,
  held: false,
  state: "idle",
  cursor: 0,
  read_cursor: 0,
  last_response_cursor: 0,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  ...overrides,
});
const state = (): State => ({
  agents: { a: agent() },
  projects: [],
  pullRequests: {},
  models: [],
  transcripts: {},
  cursors: {},
  ready: {},
  readPending: {},
  outgoing: {},
  loaded: true,
  status: "live",
  error: null,
});
const event = (
  cursor: number,
  type: Event["type"],
  data: Event["data"],
): Event => ({
  agent_id: "a",
  cursor,
  type,
  data,
  created_at: "2026-01-01T00:00:00Z",
});
const queued = {
  id: "m",
  text: "Hello",
  status: "pending" as const,
  created_at: "2026-01-01T00:00:00Z",
};
const output = {
  Content: "Hello back",
  ResponseType: "agent" as const,
  ToolCallID: "",
  ToolName: "",
  FullToolOutput: "",
};

describe("event replay", () => {
  it("deduplicates replay, ignores conversation checkpoints for display, and updates queue entries", () => {
    let s = reduceEvent(state(), event(1, "message.queued", queued));
    expect(s.transcripts.a || []).toHaveLength(0);
    expect(s.agents.a.queue![0].status).toBe("pending");
    expect(reduceEvent(s, event(1, "message.queued", queued))).toBe(s);
    s = reduceEvent(
      s,
      event(2, "turn.started", { ...queued, status: "running" }),
    );
    s = reduceEvent(s, event(3, "output", output));
    s = reduceEvent(
      s,
      event(4, "conversation", [{ role: "assistant", content: "Hello back" }]),
    );
    s = reduceEvent(
      s,
      event(5, "turn.completed", { ...queued, status: "completed" }),
    );
    expect(s.transcripts.a.map((m) => m.text)).toEqual(["Hello", "Hello back"]);
    expect(s.agents.a.queue).toHaveLength(1);
    expect(s.agents.a.queue![0].status).toBe("completed");
    expect(s.cursors.a).toBe(5);
  });
  it("renders bot turns once with sender provenance across queue and history events", () => {
    const bot = {
      ...queued,
      kind: "bot" as const,
      sender_agent_id: "review-chat",
    };
    let s = reduceEvent(state(), event(1, "message.queued", bot));
    expect(s.transcripts.a || []).toHaveLength(0);
    expect(s.agents.a.queue![0]).toMatchObject({
      kind: "bot",
      sender_agent_id: "review-chat",
      status: "pending",
    });
    s = reduceEvent(s, event(2, "turn.started", { ...bot, status: "running" }));
    expect(s.transcripts.a).toEqual([
      expect.objectContaining({
        id: "2",
        kind: "bot",
        text: "Hello",
        senderAgentId: "review-chat",
      }),
    ]);
    s = reduceEvent(
      s,
      event(3, "conversation", [
        {
          role: "user",
          kind: "bot",
          sender_agent_id: "review-chat",
          content: "Hello",
        },
      ]),
    );
    expect(s.transcripts.a).toHaveLength(1);
  });

  it("rejects gaps without advancing a processed cursor", () => {
    const s = state();
    expect(() => reduceEvent(s, event(2, "output", output))).toThrow(
      "Event gap",
    );
    expect(s.cursors.a).toBeUndefined();
  });
  it("does not roll inventory back during replay; keeps the scroller mounted after initial replay", () => {
    const s = state();
    s.agents.a = agent("a", { cursor: 2, settled: true });
    const next = reduceEvent(
      s,
      event(1, "agent.updated", {
        ...agent(),
        active_settings: null,
        context_usage: {
          model: "m",
          input_tokens: 0,
          output_tokens: 0,
          estimated_tokens: 0,
          context_window: 0,
          known: false,
          estimated: false,
        },
        settled: false,
      }),
    );
    expect(next.agents.a.settled).toBe(true);
    expect(next.ready.a).toBe(false);
    const ready = reduceEvent(next, event(2, "output", output));
    expect(ready.ready.a).toBe(true);
    ready.agents.a.cursor = 4;
    expect(reduceEvent(ready, event(3, "output", output)).ready.a).toBe(true);
  });
  it("merges parallel tool results by ID while retaining the call row and replay safety", () => {
    let s = state();
    for (const [cursor, id] of [
      [1, "one"],
      [2, "two"],
    ] as const) {
      s = reduceEvent(
        s,
        event(cursor, "output", {
          ...output,
          ResponseType: "tool",
          ToolName: "bash",
          ToolCallID: id,
          Content: `echo ${id}`,
        }),
      );
    }
    const before = s;
    const result = event(3, "output", {
      ...output,
      ResponseType: "tool_result",
      ToolCallID: "two",
      Content: "capped",
      FullToolOutput: "full output",
    });
    s = reduceEvent(s, result);
    expect(s.transcripts.a).toHaveLength(2);
    expect(s.transcripts.a[0].output).toBeUndefined();
    expect(s.transcripts.a[1]).toMatchObject({
      id: "2",
      text: "echo two",
      output: "full output",
    });
    expect(before.transcripts.a[1].output).toBeUndefined();
    expect(reduceEvent(s, result)).toBe(s);
    s = reduceEvent(
      s,
      event(4, "output", {
        ...output,
        ResponseType: "tool_result",
        ToolCallID: "one",
        Content: "",
        FullToolOutput: "",
      }),
    );
    expect(s.transcripts.a[0].output).toBe("");
  });
  it("retains uncapped tool output and failed turn diagnostics", () => {
    let s = reduceEvent(
      state(),
      event(1, "output", {
        ...output,
        ResponseType: "tool_result",
        Content: "capped",
        FullToolOutput: "full output",
      }),
    );
    s = reduceEvent(
      s,
      event(2, "turn.failed", {
        ...queued,
        status: "failed",
        error: "Provider error",
      }),
    );
    expect(s.transcripts.a.map((m) => m.text)).toEqual([
      "full output",
      "Provider error",
    ]);
  });
});

describe("workspace", () => {
  it("derives names from directory paths", () => {
    expect(projectName("/new-pool/network/Github/harness/")).toBe("harness");
    expect(projectName(" C:\\Users\\dev\\harness\\ ")).toBe("harness");
    expect(projectName("/")).toBe("");
  });
  it("puts misc first, groups only active agents, and orders by creation time", () => {
    const projects: Project[] = [
      {
        id: "p",
        name: "harness",
        root: "/repo",
        defaults: { model: "m", effort: "" },
      },
    ];
    const groups = groupAgents(
      [
        agent("old"),
        agent("new", { created_at: "2026-02-01T00:00:00Z" }),
        agent("misc", { project_id: "" }),
        agent("settled", { settled: true }),
      ],
      projects,
    );
    expect(groups.misc.map((a) => a.id)).toEqual(["misc"]);
    expect(groups.projects[0].agents.map((a) => a.id)).toEqual(["new", "old"]);
    expect(groups.settled.map((a) => a.id)).toEqual(["settled"]);
  });
  it("nests sorted descendants under the family root across projects", () => {
    const projects: Project[] = [
      {
        id: "p",
        name: "root project",
        root: "/root",
        defaults: { model: "m", effort: "" },
      },
      {
        id: "q",
        name: "child project",
        root: "/child",
        defaults: { model: "m", effort: "" },
      },
    ];
    const parented = (value: Agent, parent_agent_id: string): Agent => ({
      ...value,
      parent_agent_id,
    });
    const root = agent("root", { created_at: "2026-01-01T00:00:00Z" });
    const oldChild = parented(
      agent("old-child", {
        project_id: "q",
        created_at: "2026-01-02T00:00:00Z",
      }),
      root.id,
    );
    const newChild = parented(
      agent("new-child", { created_at: "2026-01-03T00:00:00Z" }),
      root.id,
    );
    const grandchild = parented(
      agent("grandchild", { created_at: "2026-01-04T00:00:00Z" }),
      oldChild.id,
    );
    const orphan = parented(
      agent("orphan", {
        project_id: "q",
        created_at: "2026-01-05T00:00:00Z",
      }),
      "missing",
    );

    const groups = groupAgents(
      [oldChild, orphan, grandchild, root, newChild],
      projects,
    );

    expect(groups.projects[0].agents.map((a) => a.id)).toEqual(["root"]);
    expect(groups.projects[1].agents.map((a) => a.id)).toEqual(["orphan"]);
    expect(groups.children.get("root")?.map((a) => a.id)).toEqual([
      "new-child",
      "old-child",
    ]);
    expect(groups.children.get("old-child")?.map((a) => a.id)).toEqual([
      "grandchild",
    ]);
  });

  it("keeps every agent visible when inventory contains invalid parent cycles", () => {
    const groups = groupAgents(
      [
        agent("a", { parent_agent_id: "b" }),
        agent("b", { parent_agent_id: "a" }),
        agent("child", { parent_agent_id: "a" }),
        agent("self", { parent_agent_id: "self" }),
      ],
      [],
    );
    expect(groups.misc.map((a) => a.id)).toEqual(["a", "b", "self"]);
    expect(groups.children.get("a")?.map((a) => a.id)).toEqual(["child"]);
  });

  it("keeps mixed active families together and settles only the whole family", () => {
    const projects: Project[] = [
      {
        id: "p",
        name: "harness",
        root: "/repo",
        defaults: { model: "m", effort: "" },
      },
    ];
    const parented = (value: Agent, parent_agent_id: string): Agent => ({
      ...value,
      parent_agent_id,
    });
    const root = agent("root", { settled: true });
    const child = parented(agent("child"), root.id);

    let groups = groupAgents([root, child], projects);
    expect(groups.projects[0].agents.map((a) => a.id)).toEqual(["root"]);
    expect(groups.children.get("root")?.map((a) => a.id)).toEqual(["child"]);
    expect(groups.settled).toEqual([]);

    groups = groupAgents([{ ...root }, { ...child, settled: true }], projects);
    expect(groups.projects[0].agents).toEqual([]);
    expect(groups.settled.map((a) => a.id)).toEqual(["root"]);
    expect(groups.children.get("root")?.map((a) => a.id)).toEqual(["child"]);
  });
});

afterEach(() => vi.unstubAllGlobals());
describe("mutations", () => {
  it("ignores a delayed draft response after the workspace has locked", async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve;
          }),
      ),
    );
    const client = new ControlPlane();
    client.state = state();
    const request = client.updateWorkspace("a", {
      mode: "worktree",
      base_branch: "origin/main",
    });
    const locked = agent("a", {
      cursor: 10,
      workspace: {
        mode: "worktree",
        base_branch: "origin/main",
        locked: true,
        status: "ready",
        path: "/worktree/a",
      },
    });
    client.state = { ...client.state, agents: { a: locked } };
    finish(
      Response.json(
        agent("a", {
          cursor: 5,
          workspace: {
            mode: "worktree",
            base_branch: "origin/main",
            locked: false,
            status: "draft",
          },
        }),
      ),
    );
    await request;
    expect(client.snapshot().agents.a).toEqual(locked);
  });
  it("posts immediately and keeps an acknowledged preview until its event arrives", async () => {
    let acknowledge!: (response: Response) => void;
    const fetcher = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          acknowledge = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    client.state = state();
    const sending = client.send("a", "Hello", "key");
    expect(fetcher.mock.calls[0]).toEqual([
      "/v1/agents/a/messages",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ "Idempotency-Key": "key" }),
      }),
    ]);
    expect(client.snapshot().outgoing.a).toEqual([
      { key: "key", text: "Hello" },
    ]);
    acknowledge(Response.json(queued));
    await sending;
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(client.snapshot().outgoing.a).toEqual([
      { key: "key", text: "Hello", messageId: "m" },
    ]);
    // Identical text from another client is not our receipt.
    client.state = reduceEvent(
      client.state,
      event(1, "message.queued", { ...queued, id: "other" }),
    );
    expect(client.snapshot().outgoing.a).toHaveLength(1);
    client.state = reduceEvent(
      client.state,
      event(2, "message.queued", queued),
    );
    expect(client.snapshot().outgoing.a).toEqual([]);
    expect(client.snapshot().agents.a.queue).toHaveLength(2);
  });
  it("removes the preview when replay beats the HTTP acknowledgement", async () => {
    let acknowledge!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            acknowledge = resolve;
          }),
      ),
    );
    const client = new ControlPlane();
    client.state = state();
    const sending = client.send("a", "Hello", "key");
    client.state = reduceEvent(
      client.state,
      event(1, "message.queued", queued),
    );
    client.state = reduceEvent(
      client.state,
      event(2, "turn.started", { ...queued, status: "running" }),
    );
    acknowledge(Response.json(queued));
    await sending;
    expect(client.snapshot().outgoing.a).toEqual([]);
    expect(client.snapshot().agents.a.queue?.[0].status).toBe("running");
    expect(client.snapshot().transcripts.a.map((m) => m.text)).toEqual([
      "Hello",
    ]);
  });
  it("restores a remotely settled chat and retries only the rejected POST with the same key", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json(
          { error: { code: "settled", message: "restore first" } },
          { status: 409 },
        ),
      )
      .mockResolvedValueOnce(Response.json(agent()))
      .mockResolvedValueOnce(Response.json(queued));
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    client.state = state();
    await client.send("a", "Hello", "key");
    expect(fetcher.mock.calls.map((c) => [c[0], c[1].method])).toEqual([
      ["/v1/agents/a/messages", "POST"],
      ["/v1/agents/a", "PATCH"],
      ["/v1/agents/a/messages", "POST"],
    ]);
    for (const index of [0, 2])
      expect(fetcher.mock.calls[index][1].headers["Idempotency-Key"]).toBe(
        "key",
      );
  });
  it("restores before sending with a durable retry key, without calling Continue", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(Response.json(agent()))
      .mockResolvedValueOnce(Response.json(queued));
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    client.state = state();
    client.state.agents.a.settled = true;
    await client.send("a", "Hello", "retry-key");
    expect(fetcher.mock.calls.map((c) => [c[0], c[1].method])).toEqual([
      ["/v1/agents/a", "PATCH"],
      ["/v1/agents/a/messages", "POST"],
    ]);
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
      settled: false,
    });
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).toBe(
      "retry-key",
    );
  });
  it("never submits if restoration fails", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json(
          { error: { code: "unavailable", message: "Try again" } },
          { status: 503 },
        ),
      )
      .mockResolvedValueOnce(Response.json(agent("a", { settled: true })));
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    client.state = state();
    client.state.agents.a.settled = true;
    await expect(client.send("a", "Hello", "key")).rejects.toThrow("Try again");
    expect(fetcher.mock.calls.map((c) => [c[0], c[1].method])).toEqual([
      ["/v1/agents/a", "PATCH"],
      ["/v1/agents/a", "GET"],
    ]);
    expect(client.snapshot().outgoing.a).toEqual([]);
  });
  it("refreshes the irrevocable workspace lock after a failed first send", async () => {
    const draft = agent("a", {
      workspace: {
        mode: "worktree",
        base_branch: "origin/main",
        locked: false,
        status: "draft",
      },
    });
    const failed = agent("a", {
      workspace: {
        mode: "worktree",
        base_branch: "origin/main",
        locked: true,
        status: "failed",
        error: "fetch failed",
      },
    });
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json(
          { error: { code: "workspace_failed", message: "fetch failed" } },
          { status: 409 },
        ),
      )
      .mockResolvedValueOnce(Response.json(failed));
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    client.state = state();
    client.state.agents.a = draft;
    await expect(client.send("a", "Hello", "key")).rejects.toThrow(
      "fetch failed",
    );
    expect(client.snapshot().agents.a.workspace).toEqual(failed.workspace);
    expect(client.snapshot().outgoing.a).toEqual([]);
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "/v1/agents/a/messages",
      "/v1/agents/a",
    ]);
  });
  it("creates agents with only a project and idempotency key", async () => {
    const fetcher = vi.fn().mockResolvedValue(Response.json(agent()));
    vi.stubGlobal("fetch", fetcher);
    await new ControlPlane().createAgent("p", "key");
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
      project_id: "p",
    });
    expect(fetcher.mock.calls[0][1].headers["Idempotency-Key"]).toBe("key");
  });
  it("patches an empty agent workspace and stores the returned lock state", async () => {
    const updated = {
      ...agent("a"),
      workspace: {
        mode: "worktree",
        base_branch: "origin/release",
        locked: false,
        status: "draft",
      },
    };
    const fetcher = vi.fn().mockResolvedValue(Response.json(updated));
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    await client.updateWorkspace("a", {
      mode: "worktree",
      base_branch: "origin/release",
    });
    expect(fetcher).toHaveBeenCalledWith(
      "/v1/agents/a/workspace",
      expect.objectContaining({ method: "PATCH" }),
    );
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
      mode: "worktree",
      base_branch: "origin/release",
    });
    expect((client.snapshot().agents.a as any).workspace).toEqual(
      updated.workspace,
    );
  });
});

describe("WebSocket cursor safety", () => {
  class Socket {
    static OPEN = 1;
    static instances: Socket[] = [];
    readyState = 0;
    onopen?: () => void;
    onclose?: () => void;
    onmessage?: (message: { data: string }) => void;
    sent: any[] = [];
    constructor() {
      Socket.instances.push(this);
    }
    send(raw: string) {
      this.sent.push(JSON.parse(raw));
    }
    open() {
      this.readyState = 1;
      this.onopen?.();
    }
    close() {
      this.readyState = 3;
      this.onclose?.();
    }
    frame(frame: unknown) {
      this.onmessage?.({ data: JSON.stringify(frame) });
    }
  }
  async function setup(reference: () => Promise<Response>) {
    Socket.instances = [];
    vi.stubGlobal("WebSocket", Socket);
    vi.stubGlobal("location", { href: "http://localhost/", protocol: "http:" });
    const fetcher = vi.fn((path: string) => {
      if (path.includes("/events/")) return reference();
      return Promise.resolve(
        Response.json(
          path.includes("/agents")
            ? [agent()]
            : path === "/v1/projects"
              ? [
                  {
                    id: "p",
                    name: "p",
                    root: "/srv/p",
                    defaults: { model: "test/model", effort: "high" },
                  },
                ]
              : [],
        ),
      );
    });
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    await client.start();
    const socket = Socket.instances[0];
    socket.open();
    socket.frame({ type: "subscribed", request_id: "sub" });
    return { client, socket, fetcher };
  }
  it("serializes event references before following frames, never acknowledging inventory", async () => {
    let resolve!: (response: Response) => void;
    const reference = new Promise<Response>((r) => {
      resolve = r;
    });
    const { client, socket, fetcher } = await setup(() => reference);
    try {
      socket.frame({ type: "inventory", agent: agent("a", { cursor: 2 }) });
      socket.frame({
        type: "event_ref",
        agent_id: "a",
        cursor: 1,
        url: "https://untrusted.example/ignore",
      });
      socket.frame({ type: "event", event: event(2, "output", output) });
      await vi.waitFor(() =>
        expect(fetcher).toHaveBeenCalledWith(
          "/v1/agents/a/events/1",
          expect.anything(),
        ),
      );
      expect(client.state.cursors.a).toBeUndefined();
      resolve(Response.json(event(1, "message.queued", queued)));
      await vi.waitFor(() => expect(client.state.cursors.a).toBe(2));
      expect(client.state.transcripts.a.map((m) => m.text)).toEqual([
        "Hello back",
      ]);
      expect(client.state.agents.a.queue![0].status).toBe("pending");
      client.select("a");
      expect(socket.sent.at(-1).cursors).toEqual({ a: 2 });
    } finally {
      client.stop();
    }
  });
  it("stops processing queued frames when a reference fetch fails, preserving the replay cursor", async () => {
    const { client, socket } = await setup(async () => {
      throw new Error("offline");
    });
    try {
      socket.frame({
        type: "event",
        event: event(1, "message.queued", queued),
      });
      socket.frame({
        type: "event_ref",
        agent_id: "a",
        cursor: 2,
        url: "/ignored",
      });
      socket.frame({ type: "event", event: event(3, "output", output) });
      await vi.waitFor(() => expect(client.state.status).toBe("offline"));
      expect(client.state.cursors.a).toBe(1);
      expect(client.state.transcripts.a || []).toHaveLength(0);
      expect(client.state.agents.a.queue).toHaveLength(1);
      expect(socket.readyState).toBe(3);
    } finally {
      client.stop();
    }
  });
});

describe("lifecycle controls", () => {
  it("targets a specific running turn and cancels only the chosen pending entry", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation(async (_path: string, options: RequestInit) =>
        options.method === "DELETE"
          ? new Response(null, { status: 204 })
          : Response.json(agent()),
      );
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    await client.stopTurn("a", "running-turn");
    await client.cancel("a", "pending-message");
    await client.continue("a");
    await client.settings("a", { model: "new/model", effort: "high" });
    expect(fetcher.mock.calls.map((c) => [c[0], c[1].method])).toEqual([
      ["/v1/agents/a/stop", "POST"],
      ["/v1/agents/a/messages/pending-message", "DELETE"],
      ["/v1/agents/a/continue", "POST"],
      ["/v1/agents/a/settings", "PATCH"],
    ]);
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
      turn_id: "running-turn",
    });
    expect(fetcher.mock.calls[2][1].body).toBeUndefined();
  });
});

describe("legacy tool correlation", () => {
  it("merges results with old calls lacking IDs without mixing explicit IDs", () => {
    let s = reduceEvent(
      state(),
      event(1, "output", {
        ...output,
        ResponseType: "tool",
        Content: 'Ran shell command - "pwd"',
      }),
    );
    s = reduceEvent(
      s,
      event(2, "output", {
        ...output,
        ResponseType: "tool_result",
        ToolCallID: "legacy",
        ToolName: "bash",
        Content: "/srv",
      }),
    );
    expect(s.transcripts.a).toHaveLength(1);
    expect(s.transcripts.a[0].output).toBe("/srv");
    s = reduceEvent(
      s,
      event(3, "output", {
        ...output,
        ResponseType: "tool",
        ToolCallID: "new",
        Content: "ls",
      }),
    );
    s = reduceEvent(
      s,
      event(4, "output", {
        ...output,
        ResponseType: "tool_result",
        ToolCallID: "other",
        Content: "unmatched",
      }),
    );
    expect(s.transcripts.a).toHaveLength(3);
    expect(s.transcripts.a[1].output).toBeUndefined();
  });
});

describe("sidebar branch refresh", () => {
  const projects: Project[] = ["p", "q"].map((id) => ({
    id,
    name: id,
    root: `/srv/${id}`,
    defaults: { model: "test/model", effort: "high" },
  }));
  function prepare(fetcher: ReturnType<typeof vi.fn>) {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("location", { href: "http://localhost/" });
    vi.stubGlobal(
      "WebSocket",
      class {
        static OPEN = 1;
        static instances: any[] = [];
        readyState = 0;
        onmessage?: (message: { data: string }) => void;
        onclose?: () => void;
        constructor() {
          (WebSocket as any).instances.push(this);
        }
        close() {
          this.onclose?.();
        }
      },
    );
    return new ControlPlane();
  }
  it("preserves an in-flight preview through restart and reconciles replay before acknowledgement", async () => {
    let acknowledge!: (response: Response) => void;
    const fetcher = vi.fn(async (path: string) => {
      if (path === "/v1/agents/a/messages")
        return new Promise<Response>((resolve) => {
          acknowledge = resolve;
        });
      if (path.startsWith("/v1/agents")) return Response.json([agent()]);
      if (path === "/v1/projects") return Response.json(projects);
      if (path === "/v1/models") return Response.json([]);
      return Response.json(projects[0]);
    });
    const client = prepare(fetcher);
    client.state = state();
    const sending = client.send("a", "Hello", "key");
    expect(client.state.outgoing.a).toEqual([{ key: "key", text: "Hello" }]);
    try {
      await client.start();
      expect(client.state.outgoing.a).toHaveLength(1);
      (WebSocket as any).instances[0].onmessage({
        data: JSON.stringify({
          type: "event",
          event: event(1, "message.queued", queued),
        }),
      });
      await vi.advanceTimersByTimeAsync(0);
      expect(client.state.outgoing.a).toHaveLength(1);
      acknowledge(Response.json(queued));
      await sending;
      expect(client.state.outgoing.a).toEqual([]);
    } finally {
      client.stop();
      vi.useRealTimers();
    }
  });
  it.each([
    { initialRace: false, failReload: false },
    { initialRace: true, failReload: false },
    { initialRace: false, failReload: true },
  ])(
    "rebuilds after deletion found by project polling: %j",
    async ({ initialRace, failReload }) => {
      let deleted = false;
      let failed = false;
      const fetcher = vi.fn(async (path: string) => {
        if (path.startsWith("/v1/agents"))
          return Response.json(deleted ? [] : [agent()]);
        if (path === "/v1/projects")
          return Response.json(deleted || initialRace ? [] : [projects[0]]);
        if (path === "/v1/models") {
          if (deleted && failReload && !failed) {
            failed = true;
            return Response.json(
              { error: { code: "internal", message: "temporary failure" } },
              { status: 503 },
            );
          }
          return Response.json([]);
        }
        return Response.json(projects[0]);
      });
      const client = prepare(fetcher);
      try {
        await client.start();
        expect(client.state.agents.a).toBeDefined();
        (WebSocket as any).instances[0].onmessage({
          data: JSON.stringify({
            type: "error",
            code: "cursor_invalid",
            message: "agent deleted",
          }),
        });
        await vi.advanceTimersByTimeAsync(0);
        expect(client.state.status).toBe("offline");
        deleted = true;
        await vi.advanceTimersByTimeAsync(15000);
        if (failReload) {
          expect(client.state.status).toBe("offline");
          expect(client.state.error).toContain("temporary failure");
          await vi.advanceTimersByTimeAsync(1000);
        }
        expect(client.state.projects).toEqual([]);
        expect(client.state.agents).toEqual({});
        expect(
          fetcher.mock.calls.filter(
            ([path]) => path === "/v1/agents?include_settled=true",
          ),
        ).toHaveLength(failReload ? 3 : 2);
        fetcher.mockClear();
        await vi.advanceTimersByTimeAsync(15000);
        expect(
          fetcher.mock.calls.some(([path]) => path === "/v1/projects"),
        ).toBe(true);
      } finally {
        client.stop();
        vi.useRealTimers();
      }
    },
  );
  it("refreshes PRs for unselected and settled threads, clears misses, and tolerates unavailable GitHub metadata", async () => {
    let association: { branch: string; number?: number } = {
      branch: "ted/feature",
      number: 42,
    };
    let unavailable = false;
    const fetcher = vi.fn(async (path: string) => {
      if (path.endsWith("/pull-request")) {
        if (unavailable)
          return Response.json(
            { error: { message: "offline" } },
            { status: 503 },
          );
        return Response.json(association);
      }
      if (path.startsWith("/v1/agents"))
        return Response.json([agent(), agent("b", { settled: true })]);
      if (path === "/v1/projects") return Response.json(projects);
      if (path === "/v1/models") return Response.json([]);
      return Response.json(projects[0]);
    });
    const client = prepare(fetcher);
    try {
      await client.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(client.state.pullRequests).toEqual({
        a: association,
        b: association,
      });
      association = { branch: "ted/feature" };
      await vi.advanceTimersByTimeAsync(30000);
      expect(client.state.pullRequests.a.number).toBeUndefined();
      association = { branch: "ted/feature", number: 43 };
      await vi.advanceTimersByTimeAsync(30000);
      expect(client.state.pullRequests.b.number).toBe(43);
      unavailable = true;
      await vi.advanceTimersByTimeAsync(30000);
      expect(client.state.pullRequests).toEqual({ a: {}, b: {} });
      expect(client.state.error).toBeNull();
      client.stop();
      const calls = fetcher.mock.calls.length;
      await vi.advanceTimersByTimeAsync(30000);
      expect(fetcher.mock.calls).toHaveLength(calls);
    } finally {
      client.stop();
      vi.useRealTimers();
    }
  });
  it("starts each periodic PR refresh 30 seconds after the previous one completes", async () => {
    let requests = 0;
    const worktree = agent("a", {
      workspace: {
        mode: "worktree",
        locked: true,
        status: "ready",
        path: "/srv/worktree",
        branch: "feature",
      },
    });
    const fetcher = vi.fn(async (path: string) => {
      if (path.endsWith("/pull-request")) {
        requests++;
        return new Promise<Response>((resolve) => {
          setTimeout(
            () =>
              resolve(Response.json({ branch: "feature", number: requests })),
            5000,
          );
        });
      }
      if (path.startsWith("/v1/agents")) return Response.json([worktree]);
      if (path === "/v1/projects") return Response.json(projects);
      if (path === "/v1/models") return Response.json([]);
      return Response.json(projects[0]);
    });
    const client = prepare(fetcher);
    try {
      await client.start();
      expect(requests).toBe(1);
      await vi.advanceTimersByTimeAsync(5000);
      expect(client.state.pullRequests.a.number).toBe(1);
      await vi.advanceTimersByTimeAsync(29999);
      expect(requests).toBe(1);
      await vi.advanceTimersByTimeAsync(1);
      expect(requests).toBe(2);
    } finally {
      client.stop();
      vi.useRealTimers();
    }
  });
  it("deduplicates pending PR lookups and ignores results from a previous connection", async () => {
    let resolveOld!: (response: Response) => void;
    const old = new Promise<Response>((resolve) => {
      resolveOld = resolve;
    });
    let requests = 0;
    const fetcher = vi.fn(async (path: string) => {
      if (path.endsWith("/pull-request"))
        return ++requests === 1
          ? old
          : Response.json({ branch: "main", number: 43 });
      if (path.startsWith("/v1/agents")) return Response.json([agent()]);
      if (path === "/v1/projects") return Response.json(projects);
      if (path === "/v1/models") return Response.json([]);
      return Response.json(projects[0]);
    });
    const client = prepare(fetcher);
    try {
      await client.start();
      client.select("a");
      await vi.advanceTimersByTimeAsync(30000);
      expect(requests).toBe(1);
      await client.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(client.state.pullRequests.a.number).toBe(43);
      resolveOld(Response.json({ branch: "main", number: 42 }));
      await vi.advanceTimersByTimeAsync(0);
      expect(client.state.pullRequests.a.number).toBe(43);
    } finally {
      client.stop();
      vi.useRealTimers();
    }
  });
  it("refreshes a PR immediately after a workspace patch changes its branch source", async () => {
    let pullRequestCalls = 0;
    let current = agent("a", {
      workspace: {
        mode: "worktree",
        locked: false,
        status: "draft",
        base_branch: "origin/main",
      },
    });
    const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
      if (path.endsWith("/pull-request")) {
        pullRequestCalls++;
        return Response.json(
          current.workspace?.mode === "current_checkout"
            ? { branch: "main", number: 61 }
            : {},
        );
      }
      if (path === "/v1/agents/a/workspace" && options?.method === "PATCH") {
        current = {
          ...current,
          workspace: {
            mode: "current_checkout",
            locked: false,
            status: "draft",
          },
        };
        return Response.json(current);
      }
      if (path.startsWith("/v1/agents")) return Response.json([current]);
      if (path === "/v1/projects")
        return Response.json([{ ...projects[0], git_branch: "main" }]);
      if (path === "/v1/models") return Response.json([]);
      return Response.json({ ...projects[0], git_branch: "main" });
    });
    const client = prepare(fetcher);
    try {
      await client.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(pullRequestCalls).toBe(1);

      await client.updateWorkspace("a", { mode: "current_checkout" });
      await vi.advanceTimersByTimeAsync(0);
      expect(pullRequestCalls).toBe(2);
      expect(client.state.pullRequests.a).toEqual({
        branch: "main",
        number: 61,
      });
    } finally {
      client.stop();
      vi.useRealTimers();
    }
  });
  it("reruns a PR lookup immediately when its branch changes in flight", async () => {
    let resolveFirst!: (response: Response) => void;
    const first = new Promise<Response>((resolve) => {
      resolveFirst = resolve;
    });
    let requests = 0;
    const initial = agent("a", {
      workspace: {
        mode: "worktree",
        locked: true,
        status: "ready",
        path: "/srv/worktree",
        branch: "branch-a",
      },
    });
    const fetcher = vi.fn(async (path: string) => {
      if (path.endsWith("/pull-request")) {
        requests++;
        return requests === 1
          ? first
          : Response.json({ branch: "branch-b", number: 52 });
      }
      if (path.startsWith("/v1/agents")) return Response.json([initial]);
      if (path === "/v1/projects") return Response.json(projects);
      if (path === "/v1/models") return Response.json([]);
      return Response.json(projects[0]);
    });
    const client = prepare(fetcher);
    try {
      await client.start();
      expect(requests).toBe(1);
      (WebSocket as any).instances[0].onmessage({
        data: JSON.stringify({
          type: "inventory",
          agent: {
            ...initial,
            workspace: { ...initial.workspace, branch: "branch-b" },
          },
        }),
      });
      await vi.advanceTimersByTimeAsync(0);
      expect(client.state.agents.a.workspace?.branch).toBe("branch-b");
      expect(requests).toBe(1);

      resolveFirst(Response.json({ branch: "branch-a", number: 51 }));
      await vi.advanceTimersByTimeAsync(0);
      expect(requests).toBe(2);
      expect(client.state.pullRequests.a).toEqual({
        branch: "branch-b",
        number: 52,
      });
    } finally {
      client.stop();
      vi.useRealTimers();
    }
  });
  it("never renders a PR for a missing, changed, or unavailable branch", () => {
    const association = { branch: "ted/feature", number: 42 };
    expect(branchPullRequest(association, "ted/feature")).toBe(42);
    expect(branchPullRequest(association, "main")).toBeUndefined();
    expect(branchPullRequest(association, undefined)).toBeUndefined();
    expect(branchPullRequest(undefined, "ted/feature")).toBeUndefined();
    expect(
      branchPullRequest({ branch: "ted/feature" }, "ted/feature"),
    ).toBeUndefined();
  });
  it("refreshes all represented projects once, including unselected and settled chats, and clears removed branches", async () => {
    const branches: Record<string, string | undefined> = {
      p: "main",
      q: "feature/other",
    };
    const fetcher = vi.fn(async (path: string) => {
      if (path.startsWith("/v1/agents"))
        return Response.json([
          agent("a"),
          agent("b"),
          agent("c", { project_id: "q", settled: true }),
        ]);
      if (path === "/v1/projects") return Response.json(projects);
      if (path === "/v1/models") return Response.json([]);
      const id = path.split("/").pop()!;
      return Response.json({
        ...projects.find((p) => p.id === id),
        git_branch: branches[id],
        defaults: { model: "stale/model", effort: "low" },
      });
    });
    const client = prepare(fetcher);
    try {
      await client.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(client.state.projects.map((p) => p.git_branch)).toEqual([
        "main",
        "feature/other",
      ]);
      expect(
        fetcher.mock.calls.filter(([p]) => p === "/v1/projects/p"),
      ).toHaveLength(1);
      expect(
        fetcher.mock.calls.filter(([p]) => p === "/v1/projects/q"),
      ).toHaveLength(1);
      // Branch-only refresh must not overwrite a project's defaults with a stale response.
      expect(client.state.projects[0].defaults.model).toBe("test/model");
      branches.p = "feature/next";
      branches.q = undefined;
      await vi.advanceTimersByTimeAsync(3000);
      expect(client.state.projects.map((p) => p.git_branch)).toEqual([
        "feature/next",
        undefined,
      ]);
    } finally {
      client.stop();
      vi.useRealTimers();
    }
  });
  it("deduplicates in-flight branch requests and ignores responses from an earlier connection", async () => {
    let resolveOld!: (response: Response) => void;
    const old = new Promise<Response>((resolve) => {
      resolveOld = resolve;
    });
    let branchRequests = 0;
    const fetcher = vi.fn(async (path: string) => {
      if (path.startsWith("/v1/agents")) return Response.json([agent()]);
      if (path === "/v1/projects") return Response.json(projects);
      if (path === "/v1/models") return Response.json([]);
      if (++branchRequests === 1) return old;
      return Response.json({ ...projects[0], git_branch: "fresh" });
    });
    const client = prepare(fetcher);
    try {
      await client.start();
      client.select("a");
      await vi.advanceTimersByTimeAsync(3000);
      expect(branchRequests).toBe(1);
      await client.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(client.state.projects[0].git_branch).toBe("fresh");
      resolveOld(Response.json({ ...projects[0], git_branch: "stale" }));
      await vi.advanceTimersByTimeAsync(0);
      expect(client.state.projects[0].git_branch).toBe("fresh");
    } finally {
      client.stop();
      vi.useRealTimers();
    }
  });
});

describe("read receipts", () => {
  it("only agent responses mark a chat unread; replay cannot undo a receipt", () => {
    let s = state();
    s.agents.a = agent("a", { read_cursor: 1, last_response_cursor: 1 });
    s = reduceEvent(s, event(1, "output", output));
    expect(isUnread(s.agents.a)).toBe(false);
    s = reduceEvent(s, event(2, "output", { ...output, ResponseType: "tool" }));
    s = reduceEvent(
      s,
      event(3, "output", { ...output, ResponseType: "tool_result" }),
    );
    s = reduceEvent(
      s,
      event(4, "output", { ...output, ResponseType: "usage" }),
    );
    expect(isUnread(s.agents.a)).toBe(false);
    s = reduceEvent(s, event(5, "output", output));
    expect(isUnread(s.agents.a)).toBe(true);
    s = reduceEvent(
      s,
      event(6, "agent.updated", {
        ...s.agents.a,
        read_cursor: 5,
      } as Schema["AgentUpdate"]),
    );
    expect(isUnread(s.agents.a)).toBe(false);
    s = reduceEvent(
      s,
      event(7, "agent.updated", {
        ...s.agents.a,
        read_cursor: 1,
        last_response_cursor: 1,
      } as Schema["AgentUpdate"]),
    );
    expect(s.agents.a.read_cursor).toBe(5);
    expect(s.agents.a.last_response_cursor).toBe(5);
    expect(isUnread(s.agents.a)).toBe(false);
  });

  it("deduplicates reads and does not hide a response newer than the requested cursor", async () => {
    let finish!: (response: Response) => void;
    const fetcher = vi.fn(
      (_path: string, _options: RequestInit) =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    client.state = state();
    client.state.agents.a = agent("a", { cursor: 5, last_response_cursor: 5 });
    const first = client.markRead("a", 5);
    expect(client.markRead("a", 5)).toBe(first);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(JSON.parse(fetcher.mock.calls[0][1].body as string)).toEqual({
      read_cursor: 5,
    });
    expect(isUnread(client.state.agents.a, client.state.readPending.a)).toBe(
      false,
    );
    client.state.agents.a = agent("a", {
      cursor: 8,
      last_response_cursor: 8,
      read_cursor: 7,
    });
    expect(isUnread(client.state.agents.a, client.state.readPending.a)).toBe(
      true,
    );
    finish(
      Response.json(
        agent("a", { cursor: 6, read_cursor: 5, last_response_cursor: 5 }),
      ),
    );
    await first;
    expect(client.state.agents.a.last_response_cursor).toBe(8);
    expect(client.state.agents.a.read_cursor).toBe(7);
    expect(client.state.readPending.a).toBe(0);
    expect(isUnread(client.state.agents.a)).toBe(true);
    await client.markRead("a", 5);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("restores the unread indicator if saving a read fails, and allows retry", async () => {
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new Error("Offline"))
      .mockResolvedValueOnce(
        Response.json(
          agent("a", { cursor: 3, last_response_cursor: 2, read_cursor: 2 }),
        ),
      );
    vi.stubGlobal("fetch", fetcher);
    const client = new ControlPlane();
    client.state = state();
    client.state.agents.a = agent("a", { cursor: 2, last_response_cursor: 2 });
    await expect(client.markRead("a", 2)).rejects.toThrow("Offline");
    expect(isUnread(client.state.agents.a, client.state.readPending.a)).toBe(
      true,
    );
    await client.markRead("a", 2);
    expect(isUnread(client.state.agents.a, client.state.readPending.a)).toBe(
      false,
    );
  });
});
