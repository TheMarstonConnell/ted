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
  const defaults = project.workspace_defaults;
  if (!defaults) return undefined;
  // Projects saved before workspace support can have an empty mode. Match
  // the server's normalization rather than passing an unmatched select value.
  return { ...defaults, mode: defaults.mode || "current_checkout" };
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
/**
 * Group agent families without flattening their parent/child relationships.
 *
 * The three arrays contain family roots. Immediate descendants are available in
 * `children`, and can be followed recursively for grandchildren. A family stays
 * in its root's project while any member is active; only a completely settled
 * family moves to Settled. This keeps an active child reachable when its parent
 * has already been settled and also keeps children with explicit workspaces
 * nested even when their project metadata differs from the root's.
 */
export function groupAgents(agents: Agent[], projects: Project[]) {
  const sorted = [...agents].sort(
    (a, b) =>
      b.created_at.localeCompare(a.created_at) || a.id.localeCompare(b.id),
  );
  const byId = new Map(sorted.map((agent) => [agent.id, agent]));
  const parentById = new Map<string, string>();

  for (const agent of sorted) {
    const parent = agent.parent_agent_id;
    if (parent && parent !== agent.id && byId.has(parent))
      parentById.set(agent.id, parent);
  }

  // Malformed inventories must not make agents disappear. Break only links
  // originating inside a cycle; descendants can still nest under those roots.
  const cycleMembers = new Set<string>();
  const visited = new Set<string>();
  for (const agent of sorted) {
    if (visited.has(agent.id)) continue;
    const path: string[] = [];
    const positions = new Map<string, number>();
    let id: string | undefined = agent.id;
    while (id && !visited.has(id)) {
      const repeatedAt = positions.get(id);
      if (repeatedAt !== undefined) {
        path.slice(repeatedAt).forEach((member) => cycleMembers.add(member));
        break;
      }
      positions.set(id, path.length);
      path.push(id);
      id = parentById.get(id);
    }
    path.forEach((member) => visited.add(member));
  }
  cycleMembers.forEach((id) => parentById.delete(id));

  // Insertion follows the existing newest-first ordering, preserving sibling
  // order as well as the previous order of top-level chats.
  const children = new Map<string, Agent[]>();
  for (const agent of sorted) {
    const parent = parentById.get(agent.id);
    if (!parent) continue;
    const siblings = children.get(parent) || [];
    siblings.push(agent);
    children.set(parent, siblings);
  }
  const roots = sorted.filter((agent) => !parentById.has(agent.id));
  const familyIsSettled = (root: Agent): boolean =>
    root.settled &&
    (children.get(root.id) || []).every((child) => familyIsSettled(child));
  const activeRoots = roots.filter((root) => !familyIsSettled(root));
  const projectIds = new Set(projects.map((project) => project.id));

  return {
    misc: activeRoots.filter(
      (root) => !root.project_id || !projectIds.has(root.project_id),
    ),
    projects: projects.map((project) => ({
      project,
      agents: activeRoots.filter((root) => root.project_id === project.id),
    })),
    settled: roots.filter(familyIsSettled),
    children,
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
