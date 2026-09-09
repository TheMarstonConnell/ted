import { usePanel } from "@/lib/navigation";
import {
  memo,
  useCallback,
  useRef,
  useState,
  useSyncExternalStore,
  type FormEvent,
  type RefObject,
} from "react";
import { useNavigate, useParams } from "react-router-dom";
import Markdown, { type Components, type ExtraProps } from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  Archive,
  ArrowUp,
  ChevronRight,
  GitBranch,
  LoaderCircle,
  Menu,
  Play,
  Square,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Collapsible,
  CollapsibleTrigger,
  CollapsibleContent,
} from "@/components/ui/collapsible";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupTextarea,
} from "@/components/ui/input-group";
import {
  MessageScroller,
  MessageScrollerProvider,
  MessageScrollerViewport,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerButton,
  useMessageScroller,
} from "@/components/ui/message-scroller";
import { cn, toolCommand } from "@/lib/utils";
import {
  agentTitle,
  agentWorkspace,
  projectWorkspaceDefaults,
  requestKey,
  type Agent,
  type QueueMessage,
  type WorkspaceSelection,
} from "@/lib/api";
import { control, useControl, type TranscriptItem } from "@/lib/store";
import { ErrorNotice, Loading, ModelFields } from "./common";
import { ChatImage } from "./chat-image";
import {
  WorkspaceFields,
  WorkspaceIndicator,
  WorkspaceSetupBlock,
} from "./workspace";

// Intentionally ephemeral: agent switches retain drafts, a reload does not.
const drafts = new Map<string, string>();
const draftVersions = new Map<string, number>();
const receipts = new Map<string, { text: string; key: string }>();
const inFlight = new Set<string>();
const editingDrafts = new Set<string>();
const composerErrors = new Map<string, string>();
// Drafts and pending actions must stay in sync even if a request completes after
// navigating away and back to the same chat. Nothing is persisted to disk.
const composerListeners = new Set<() => void>();
const subscribeComposer = (listener: () => void) => {
  composerListeners.add(listener);
  return () => {
    composerListeners.delete(listener);
  };
};
const notifyComposer = () =>
  composerListeners.forEach((listener) => listener());
function writeDraft(agentId: string, text: string) {
  draftVersions.set(agentId, (draftVersions.get(agentId) || 0) + 1);
  drafts.set(agentId, text);
  notifyComposer();
}
const EMPTY_TRANSCRIPT: TranscriptItem[] = [];

const HELP =
  "/model [provider/model] · /effort [value] · /stop · /settle · /unsettle · /continue · /help · /exit. Use // to send a literal leading slash.";

function containsImage(node: ExtraProps["node"]): boolean {
  return (
    !!node &&
    (node.tagName === "img" ||
      node.children.some(
        (child) => child.type === "element" && containsImage(child),
      ))
  );
}

// Keep renderer identities stable so incoming agent events don't remount an
// image and close its preview. Image links open the preview instead of a new tab.
const markdownComponents: Components = {
  a: ({ node, children, ...props }) =>
    containsImage(node) ? (
      <span title={props.title}>{children}</span>
    ) : (
      <a {...props} target="_blank" rel="noopener noreferrer">
        {children}
      </a>
    ),
  img: ({ src, alt, title }) => (
    <ChatImage key={src} src={src} alt={alt} title={title} />
  ),
};

const Message = memo(function Message({ item }: { item: TranscriptItem }) {
  if (item.kind === "tool" || item.kind === "tool_result") {
    // An empty result is still a result. Do not infer success/failure from text.
    const waiting = item.kind === "tool" && item.output === undefined;
    return (
      <Collapsible disabled={waiting} className="min-w-0 rounded-lg border">
        <CollapsibleTrigger
          aria-busy={waiting}
          title={waiting ? "Waiting for tool output" : undefined}
          render={
            <Button
              variant="ghost"
              className="group h-auto w-full justify-start px-4 py-2 disabled:opacity-100"
            />
          }
        >
          {waiting ? (
            <LoaderCircle
              data-slot="tool-waiting"
              className="size-4 animate-spin motion-reduce:animate-none"
              aria-hidden="true"
            />
          ) : (
            <ChevronRight
              data-slot="tool-expand"
              className="size-4 group-data-panel-open:rotate-90"
              aria-hidden="true"
            />
          )}
          <span
            className="min-w-0 truncate font-mono text-xs font-normal"
            title={item.kind === "tool" ? toolCommand(item.text) : undefined}
          >
            {item.kind === "tool"
              ? toolCommand(item.text).replace(/\s+/g, " ").trim() ||
                item.toolName ||
                "Tool"
              : `${item.toolName || "Tool"} output`}
          </span>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <pre className="overflow-x-auto whitespace-pre-wrap break-words border-t p-4 font-mono text-xs leading-5">
            {item.kind === "tool"
              ? item.output || "No output returned."
              : item.text || "No output returned."}
          </pre>
        </CollapsibleContent>
      </Collapsible>
    );
  }
  if (item.kind === "status")
    return (
      <Alert>
        <AlertDescription>{item.text}</AlertDescription>
      </Alert>
    );
  return (
    <article
      aria-label={item.kind === "user" ? "Your message" : "Assistant message"}
      className={cn(
        "min-w-0",
        item.kind === "user" &&
          "ml-auto w-fit max-w-[90%] rounded-xl bg-muted p-4",
      )}
    >
      <div className="markdown text-sm leading-6 [overflow-wrap:anywhere] [&>*+*]:mt-4 [&_p]:whitespace-pre-wrap [&_h1]:text-xl [&_h1]:leading-7 [&_h2]:text-xl [&_h2]:leading-7 [&_h3]:text-sm [&_h1]:font-semibold [&_h2]:font-semibold [&_h3]:font-semibold [&_h4]:font-semibold [&_h5]:font-semibold [&_h6]:font-semibold [&_a]:underline [&_a]:underline-offset-4 [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_pre]:overflow-x-auto [&_pre]:rounded-lg [&_pre]:border [&_pre]:bg-muted [&_pre]:p-4 [&_pre]:text-xs [&_pre]:leading-5 [&_code]:font-mono [&_:not(pre)>code]:rounded [&_:not(pre)>code]:bg-muted [&_:not(pre)>code]:px-1 [&_:not(pre)>code]:py-0.5 [&_blockquote]:border-l-2 [&_blockquote]:pl-4 [&_blockquote]:text-muted-foreground [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto [&_th]:border [&_th]:px-4 [&_th]:py-2 [&_th]:text-left [&_td]:border [&_td]:px-4 [&_td]:py-2">
        <Markdown remarkPlugins={[remarkGfm]} components={markdownComponents}>
          {item.text}
        </Markdown>
      </div>
    </article>
  );
});

const Transcript = memo(function Transcript({
  items,
  ready,
  state,
  onScrollIntent,
}: {
  items: TranscriptItem[];
  ready: boolean;
  state: Agent["state"];
  onScrollIntent: () => void;
}) {
  return !ready ? (
    <Loading>Replaying chat history…</Loading>
  ) : (
    <MessageScroller>
      <MessageScrollerViewport
        className="workspace-scroll-gutter"
        onWheel={onScrollIntent}
        onTouchMove={onScrollIntent}
        onPointerDown={onScrollIntent}
        onFocusCapture={onScrollIntent}
        onKeyDown={(event) => {
          if (
            [
              "ArrowDown",
              "ArrowUp",
              "End",
              "Home",
              "PageDown",
              "PageUp",
              " ",
            ].includes(event.key)
          )
            onScrollIntent();
        }}
      >
        <MessageScrollerContent className="mx-auto max-w-chat gap-0 px-4 py-8 md:px-8 [&>*+*]:mt-6 [&>[data-tool=true]+[data-tool=true]]:mt-2">
          {!items.length && (
            <p className="py-12 text-center text-sm text-muted-foreground">
              Send a message to start this chat.
            </p>
          )}
          {items.map((item) => (
            <MessageScrollerItem
              key={item.id}
              messageId={item.id}
              data-tool={item.kind === "tool" || item.kind === "tool_result"}
            >
              <Message item={item} />
            </MessageScrollerItem>
          ))}
          {state !== "idle" && (
            <div
              role="status"
              className="flex items-center gap-2 py-2 text-sm leading-6 text-muted-foreground"
            >
              {state === "stopping" ? (
                "Stopping current turn…"
              ) : (
                <span className="shimmer">Ted is working…</span>
              )}
            </div>
          )}
        </MessageScrollerContent>
      </MessageScrollerViewport>
      <MessageScrollerButton />
    </MessageScroller>
  );
});

// Typing updates this small leaf, not the transcript, queue, or model menus.
const ComposerInput = memo(function ComposerInput({
  agentId,
  inputRef,
  readOnly,
  settled,
}: {
  agentId: string;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  readOnly: boolean;
  settled: boolean;
}) {
  const draft = useSyncExternalStore(
    subscribeComposer,
    () => drafts.get(agentId) || "",
  );
  return (
    <InputGroupTextarea
      ref={inputRef}
      readOnly={readOnly}
      autoFocus
      aria-label="Message"
      placeholder={
        settled ? "Send a message to restore this chat…" : "Message Ted…"
      }
      className="max-h-52 max-md:max-h-[min(13rem,25dvh)] min-h-12 px-4 pt-4 leading-6 md:min-h-16 md:px-inset md:pt-inset md:pb-4"
      value={draft}
      onChange={(event) => writeDraft(agentId, event.target.value)}
      onKeyDown={(event) => {
        if (
          event.key === "Enter" &&
          !event.shiftKey &&
          !event.nativeEvent.isComposing
        ) {
          event.preventDefault();
          event.currentTarget.form?.requestSubmit();
        }
      }}
    />
  );
});

const SendMessageButton = memo(function SendMessageButton({
  agentId,
  disabled,
}: {
  agentId: string;
  disabled: boolean;
}) {
  const empty = useSyncExternalStore(
    subscribeComposer,
    () => !drafts.get(agentId)?.trim(),
  );
  return (
    <Button
      type="submit"
      size="icon"
      disabled={disabled || empty}
      aria-label="Send message"
    >
      <ArrowUp />
    </Button>
  );
});

export function Chat() {
  // The composer and transcript share one per-chat scroller. A send can then
  // explicitly resume following, including when touch/wheel intent paused it
  // without moving the viewport. App keys Chat by agent ID.
  return (
    <MessageScrollerProvider autoScroll defaultScrollPosition="end">
      <ChatWorkspace />
    </MessageScrollerProvider>
  );
}

function ChatWorkspace() {
  const { agentId = "" } = useParams();
  const {
    agents,
    projects,
    transcripts,
    ready: replayed,
    loaded,
    status,
  } = useControl();
  const { open } = usePanel();
  const navigate = useNavigate();
  const agent = agents[agentId];
  const project = projects.find((p) => p.id === agent?.project_id);
  const items = transcripts[agentId] || EMPTY_TRANSCRIPT;
  const busy = useSyncExternalStore(subscribeComposer, () =>
    inFlight.has(agentId),
  );
  const editingDraft = useSyncExternalStore(subscribeComposer, () =>
    editingDrafts.has(agentId),
  );
  const { scrollToEnd } = useMessageScroller();
  const scrollIntentVersion = useRef(0);
  const onScrollIntent = useCallback(() => {
    scrollIntentVersion.current++;
  }, []);
  const composerRef = useRef<HTMLTextAreaElement>(null);
  const [editCandidate, setEditCandidate] = useState<QueueMessage | null>(null);
  const error = useSyncExternalStore(
    subscribeComposer,
    () => composerErrors.get(agentId) || null,
  );
  const setError = (error: string | null) => {
    if (error) composerErrors.set(agentId, error);
    else composerErrors.delete(agentId);
    notifyComposer();
  };
  const [notice, setNotice] = useState<string | null>(null);
  const ready = !!agent && replayed[agentId];
  const queue = agent?.queue || [];
  const pending = queue.filter((m) => m.status === "pending");
  const running = queue.find((m) => m.status === "running");
  const workspace = agent ? agentWorkspace(agent) : undefined;
  const workspaceSelection: WorkspaceSelection = workspace
    ? {
        mode: workspace.mode,
        ...(workspace.base_branch
          ? { base_branch: workspace.base_branch }
          : {}),
      }
    : (project && projectWorkspaceDefaults(project)) || {
        mode: "current_checkout",
      };
  // workspace is always returned by current servers. Queue inference keeps older
  // optional fixtures safely locked after their first message.
  const workspaceLocked = workspace?.locked ?? queue.length > 0;
  const workspaceBlocked =
    workspace?.status === "fetching" ||
    workspace?.status === "creating" ||
    workspace?.status === "failed";
  const context = agent?.context_usage;
  const contextPercent =
    context && context.context_window > 0
      ? Math.round((context.estimated_tokens / context.context_window) * 100)
      : 0;
  const action = async (fn: () => Promise<unknown>) => {
    if (inFlight.has(agentId)) return;
    inFlight.add(agentId);
    notifyComposer();
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(String(e));
    } finally {
      inFlight.delete(agentId);
      notifyComposer();
    }
  };
  const editPending = (message: QueueMessage) => {
    setEditCandidate(null);
    void action(async () => {
      const current = control
        .snapshot()
        .agents[agentId]?.queue?.find((m) => m.id === message.id);
      if (current?.status !== "pending")
        throw new Error("This message is no longer pending.");
      editingDrafts.add(agentId);
      notifyComposer();
      try {
        // DELETE is atomic: if this turn started in the meantime, the server
        // rejects it and the existing draft remains untouched.
        await control.cancel(agentId, message.id);
        // A previously accepted send may have left a retry receipt. Editing is
        // a new submission, not a retry of the now-cancelled queue entry.
        receipts.delete(agentId);
        // Preserve a literal leading slash rather than turning it into a command.
        writeDraft(agentId, message.text.replace(/^(\s*)\//, "$1//"));
        requestAnimationFrame(() => {
          const input = composerRef.current;
          input?.focus();
          input?.setSelectionRange(input.value.length, input.value.length);
        });
      } finally {
        editingDrafts.delete(agentId);
        notifyComposer();
      }
    });
  };
  const requestEdit = (message: QueueMessage) => {
    if (inFlight.has(agentId) || !ready || status !== "live") return;
    const text = message.text.replace(/^(\s*)\//, "$1//");
    const draft = drafts.get(agentId) || "";
    if (draft && draft !== text) setEditCandidate(message);
    else editPending(message);
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const original = drafts.get(agentId) || "";
    if (!original.trim() || busy || workspaceBlocked) return;
    void action(async () => {
      const trimmed = original.trim();
      if (trimmed.startsWith("/") && !trimmed.startsWith("//")) {
        const [command, ...args] = trimmed.slice(1).split(/\s+/);
        if (args.length > (["model", "effort"].includes(command) ? 1 : 0))
          throw new Error(`Invalid arguments for /${command}`);
        switch (command) {
          case "help":
            setNotice(HELP);
            break;
          case "exit":
            navigate("/");
            break;
          case "model":
            if (args[0]) await control.settings(agentId, { model: args[0] });
            else open("panel", "settings");
            break;
          case "effort":
            if (args[0]) await control.settings(agentId, { effort: args[0] });
            else open("panel", "settings");
            break;
          case "settle":
            await control.settle(agentId, true);
            break;
          case "unsettle":
            await control.settle(agentId, false);
            break;
          case "continue":
            await control.continue(agentId);
            break;
          case "stop":
            if (!running) throw new Error("No running turn to stop.");
            await control.stopTurn(agentId, running.id);
            break;
          default:
            throw new Error(`Unknown command /${command}. Use /help.`);
        }
      } else {
        const text = original.replace(/^(\s*)\/\//, "$1/");
        let receipt = receipts.get(agentId);
        if (!receipt || receipt.text !== text) {
          receipt = { text, key: requestKey() };
          receipts.set(agentId, receipt);
        }
        // Clear the submitted draft before the request, not after an async
        // response that may arrive after more typing or a chat switch.
        const scrollVersion = scrollIntentVersion.current;
        writeDraft(agentId, "");
        const version = draftVersions.get(agentId);
        try {
          await control.send(agentId, text, receipt.key);
          receipts.delete(agentId);
        } catch (error) {
          // Keep the retry key and restore only if the composer was untouched.
          if (draftVersions.get(agentId) === version)
            writeDraft(agentId, original);
          throw error;
        }
        // Re-enter following mode, not just a one-off scrollTop assignment:
        // queued turns and content/composer resizes may land after the ACK.
        // A user who starts reading while the request is pending takes priority.
        // On a chat switch the old provider's viewport ref is cleared, so this
        // cannot scroll another chat (or a newly mounted copy of this chat).
        if (scrollIntentVersion.current === scrollVersion) {
          scrollToEnd({ behavior: "auto" });
        }
        return;
      }
      if (drafts.get(agentId) === original) {
        writeDraft(agentId, "");
      }
    });
  };
  if (!loaded) return <Loading>Connecting to your workspace…</Loading>;
  if (!agent)
    return (
      <div className="m-auto max-w-sm space-y-4 p-8 text-center">
        <h1 className="text-lg font-semibold">Agent not found</h1>
        <p className="text-sm text-muted-foreground">
          This agent isn’t available on this server.
        </p>
        <Button onClick={() => navigate("/")}>Back to workspace</Button>
      </div>
    );
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <Dialog
        open={!!editCandidate}
        onOpenChange={(open) => {
          if (!open) setEditCandidate(null);
        }}
      >
        <DialogContent finalFocus={composerRef}>
          <DialogHeader>
            <DialogTitle>Replace current draft?</DialogTitle>
            <DialogDescription>
              Editing this message removes it from the queue and replaces your
              current draft. It won’t be sent again until you send it.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditCandidate(null)}>
              Keep draft
            </Button>
            <Button
              disabled={busy || !ready || status !== "live"}
              onClick={() => {
                if (editCandidate) editPending(editCandidate);
              }}
            >
              Edit message
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <header className="flex h-14 shrink-0 items-center gap-4 border-b border-border px-4 md:px-6">
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-sm font-semibold">
            {agentTitle(agent)}
          </h1>
        </div>
        <Button
          variant="ghost"
          size="icon"
          aria-label={agent.settled ? "Restore agent" : "Settle agent"}
          title={agent.settled ? "Restore agent" : "Settle agent"}
          disabled={busy}
          onClick={() =>
            void action(() => control.settle(agentId, !agent.settled))
          }
        >
          <Archive />
        </Button>
      </header>
      <div className="flex min-h-0 flex-1 flex-col">
        <Transcript
          items={items}
          ready={!!ready}
          state={agent.state}
          onScrollIntent={onScrollIntent}
        />
        <div className="workspace-scroll-gutter scrollbar-thin shrink-0 overflow-y-auto">
          <div className="mx-auto w-full max-w-chat space-y-4 px-4 pb-4 pt-2 md:px-8">
            <ErrorNotice error={error} />
            {workspace && workspace.status !== "draft" && (
              <WorkspaceSetupBlock workspace={workspace} />
            )}
            {notice && (
              <Alert>
                <AlertDescription>{notice}</AlertDescription>
                <Button
                  size="xs"
                  variant="ghost"
                  onClick={() => setNotice(null)}
                >
                  Dismiss
                </Button>
              </Alert>
            )}
            {agent.settled && (
              <p className="text-xs text-muted-foreground">
                This chat is settled. Sending a message restores it
                {pending.length ? " and releases queued work in order" : ""}.
              </p>
            )}
            {(pending.length > 0 || agent.held) && (
              <div
                role="region"
                aria-label="Pending messages"
                className="max-h-40 scroll-pt-8 overflow-y-auto rounded-lg border border-border bg-muted"
              >
                <div
                  data-slot="pending-queue-header"
                  className="sticky top-0 z-10 flex items-center justify-between bg-muted px-4 py-2 text-xs"
                >
                  <span className="font-medium">
                    {agent.held ? "Queue held" : "Up next"} · {pending.length}{" "}
                    pending
                  </span>
                </div>
                {pending.length > 0 && (
                  <div className="px-4 pb-2">
                    {pending.map((m) => (
                      <div
                        key={m.id}
                        data-pending-message-id={m.id}
                        className="mt-2 flex items-center gap-2 border-t border-border pt-2 text-xs first:mt-0"
                      >
                        <span
                          className="min-w-0 flex-1 truncate"
                          title={m.text}
                        >
                          {m.text}
                        </span>
                        <Button
                          type="button"
                          variant="ghost"
                          size="xs"
                          title="Move this message back to the composer"
                          disabled={busy || !ready || status !== "live"}
                          onClick={() => requestEdit(m)}
                        >
                          Edit
                        </Button>
                        <Button
                          variant="ghost"
                          size="xs"
                          disabled={busy}
                          onClick={() =>
                            void action(() => control.cancel(agentId, m.id))
                          }
                        >
                          Cancel
                        </Button>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
            {!workspaceLocked && project && (
              <WorkspaceFields
                compact
                projectId={project.id}
                value={workspaceSelection}
                disabled={busy || !ready || status !== "live"}
                onChange={(selection) =>
                  void action(() => control.updateWorkspace(agentId, selection))
                }
              />
            )}
            <form onSubmit={submit} aria-label="Message composer">
              <InputGroup
                aria-label="Message input"
                className="rounded-xl has-disabled:opacity-100 has-disabled:bg-transparent dark:has-disabled:bg-input/30"
              >
                <ComposerInput
                  agentId={agentId}
                  inputRef={composerRef}
                  readOnly={editingDraft}
                  settled={agent.settled}
                />
                <InputGroupAddon
                  align="block-end"
                  className="items-end px-4 pb-4 pt-2 md:px-inset md:pb-inset"
                  aria-label="Composer toolbar"
                >
                  <div
                    role="group"
                    aria-label="Chat settings"
                    className="flex min-w-0 flex-1 flex-wrap items-center gap-2"
                  >
                    <ModelFields
                      compact
                      disabled={
                        busy || !ready || status !== "live" || workspaceBlocked
                      }
                      model={agent.settings.model}
                      effort={agent.settings.effort}
                      onChange={(model, effort) =>
                        void action(() =>
                          control.settings(agentId, { model, effort }),
                        )
                      }
                    />
                  </div>
                  <div
                    role="group"
                    aria-label="Message actions"
                    className="ml-auto flex shrink-0 items-center gap-2"
                  >
                    {agent.held && !agent.settled && (
                      <Button
                        type="button"
                        size="icon"
                        variant="ghost"
                        aria-label="Continue"
                        title="Continue queued work"
                        disabled={
                          busy ||
                          !ready ||
                          status !== "live" ||
                          workspaceBlocked
                        }
                        onClick={() =>
                          void action(() => control.continue(agentId))
                        }
                      >
                        <Play />
                      </Button>
                    )}
                    {running && (
                      <Button
                        type="button"
                        variant="destructive"
                        size="icon"
                        aria-label="Stop"
                        disabled={busy || !ready || status !== "live"}
                        title="Stop this turn; the next pending message can then run"
                        onClick={() =>
                          void action(() =>
                            control.stopTurn(agentId, running.id),
                          )
                        }
                      >
                        <Square className="size-3 fill-current" />
                      </Button>
                    )}
                    <SendMessageButton
                      agentId={agentId}
                      disabled={
                        busy || !ready || status !== "live" || workspaceBlocked
                      }
                    />
                  </div>
                </InputGroupAddon>
              </InputGroup>
            </form>
            <div
              role="group"
              aria-label="Composer footer"
              className="flex min-w-0 items-center gap-4 border-x border-transparent px-4 md:px-inset"
            >
              {project &&
                (workspaceLocked && workspace ? (
                  <div
                    role="group"
                    aria-label="Project location"
                    className="flex min-w-0 flex-1"
                  >
                    <WorkspaceIndicator
                      workspace={workspace}
                      fallbackPath={project.root}
                      fallbackBranch={project.git_branch}
                    />
                  </div>
                ) : (
                  <div
                    role="group"
                    aria-label="Project location"
                    className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground"
                  >
                    <span
                      className="hidden min-w-0 max-w-full truncate font-mono md:inline"
                      title={project.root}
                    >
                      {project.root}
                    </span>
                    {project.git_branch && (
                      <span
                        className="flex min-w-0 max-w-full items-center gap-2"
                        title={project.git_branch}
                      >
                        <GitBranch
                          className="size-3 shrink-0"
                          aria-hidden="true"
                        />
                        <span className="truncate font-mono">
                          {project.git_branch}
                        </span>
                      </span>
                    )}
                  </div>
                ))}
              <div className="ml-auto flex shrink-0 items-center gap-2">
                <span
                  data-slot="context-usage"
                  className="font-mono text-xs text-muted-foreground"
                  title={`Context usage: ${contextPercent}%`}
                >
                  <span className="hidden md:inline">
                    Context {contextPercent}%
                  </span>
                  <span className="md:hidden">{contextPercent}%</span>
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="md:hidden"
                  aria-label="Open sidebar"
                  onClick={() => open("sidebar", "open")}
                >
                  <Menu />
                </Button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
