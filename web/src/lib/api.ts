import type { components } from "./api.generated";
export type Schema = components["schemas"];
export type Agent = Schema["Agent"];
export type Project = Schema["Project"];
export type Model = Schema["Model"];
export type Event = Schema["Event"];
export type QueueMessage = Schema["QueuedMessage"];
export type Output = Schema["AgentOutput"];

// Workspace fields remain optional in generated response types for legacy
// fixtures, although current servers always return them.
export type WorkspaceSelection = Schema["WorkspaceSelection"];
export type Workspace = Schema["Workspace"];
export type ProjectBranches = Schema["ProjectBranches"];
export function agentWorkspace(agent: Agent): Workspace | undefined {
  return agent.workspace;
}
export function projectWorkspaceDefaults(
  project: Project,
): WorkspaceSelection | undefined {
  return project.workspace_defaults;
}
export type Frame =
  | Schema["WSInventory"]
  | Schema["WSEvent"]
  | Schema["WSEventReference"]
  | Schema["WSError"]
  | Schema["WSSubscribed"]
  | Schema["WSAck"];

export class APIError extends Error {
  code: string;
  constructor(code: string, message: string) {
    super(message);
    this.code = code;
  }
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  key?: string,
): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: {
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...(key ? { "Idempotency-Key": key } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    const data = await response.json().catch(() => null);
    throw new APIError(
      data?.error?.code || String(response.status),
      data?.error?.message || response.statusText,
    );
  }
  return response.status === 204 ? (undefined as T) : response.json();
}
export const agentPath = (id: string) => `/v1/agents/${encodeURIComponent(id)}`;
export function projectName(root: string) {
  return (
    root
      .trim()
      .replace(/[\\/]+$/, "")
      .split(/[\\/]/)
      .pop() || ""
  );
}
export function groupAgents(agents: Agent[], projects: Project[]) {
  const sorted = [...agents].sort(
    (a, b) =>
      b.created_at.localeCompare(a.created_at) || a.id.localeCompare(b.id),
  );
  const active = sorted.filter((a) => !a.settled);
  return {
    misc: active.filter(
      (a) => !a.project_id || !projects.some((p) => p.id === a.project_id),
    ),
    projects: projects.map((project) => ({
      project,
      agents: active.filter((a) => a.project_id === project.id),
    })),
    settled: sorted.filter((a) => a.settled),
  };
}

export function agentTitle(agent: Agent) {
  return (
    agent.title ||
    agent.queue?.[0]?.text.replace(/\s+/g, " ").trim().slice(0, 80) ||
    "New chat"
  );
}

// randomUUID is secure-context-only; trusted LAN deployments may use plain HTTP.
export function requestKey() {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
}
