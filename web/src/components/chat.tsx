import { usePanel } from "@/lib/navigation";
import { useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import Markdown, { type Components, type ExtraProps } from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  Archive,
  ArrowUp,
  Check,
  ChevronRight,
  Copy,
  GitBranch,
  Menu,
  Play,
  Square,
} from "lucide-react";
import { Button } from "@/components/ui/button";
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
} from "@/components/ui/message-scroller";
import { cn, toolCommand } from "@/lib/utils";
import { agentTitle, requestKey } from "@/lib/api";
import { control, useControl, type TranscriptItem } from "@/lib/store";
import { ErrorNotice, Loading, ModelFields } from "./common";
import { ChatImage } from "./chat-image";

// Intentionally ephemeral: agent switches retain drafts, a reload does not.
const drafts = new Map<string, string>();
const draftVersions = new Map<string, number>();
const receipts = new Map<string, { text: string; key: string }>();
const inFlight = new Set<string>();
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

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState(false);
  return (
    <Button
      variant="ghost"
      size="icon-xs"
      aria-label={
        error
          ? "Copy failed; select the text to copy"
          : copied
            ? "Copied"
            : "Copy message"
      }
      title={error ? "Clipboard unavailable; select text to copy" : "Copy"}
      onClick={() => {
        void navigator.clipboard
          ?.writeText(text)
          .then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          })
          .catch(() => setError(true));
        if (!navigator.clipboard) setError(true);
      }}
    >
      {copied ? <Check /> : <Copy />}
    </Button>
  );
}
function Message({ item }: { item: TranscriptItem }) {
  if (item.kind === "tool" || item.kind === "tool_result")
    return (
      <Collapsible className="min-w-0 rounded-lg border">
        <CollapsibleTrigger
          render={
            <Button
              variant="ghost"
              className="group h-auto w-full justify-start px-3 py-2"
            />
          }
        >
          <ChevronRight className="size-4 group-data-panel-open:rotate-90" />
          <span
            className="min-w-0 truncate text-xs font-normal"
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
          <pre className="overflow-x-auto whitespace-pre-wrap break-words border-t p-3 text-xs leading-6">
            {item.kind === "tool"
              ? (item.output ?? "Waiting for output…")
              : item.text}
          </pre>
        </CollapsibleContent>
      </Collapsible>
    );
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
          "ml-auto w-fit max-w-[90%] rounded-lg bg-muted px-4 py-3",
      )}
    >
      <div className="markdown text-sm leading-7 [overflow-wrap:anywhere] [&>*+*]:mt-4 [&_p]:whitespace-pre-wrap [&_h1]:text-2xl [&_h2]:text-xl [&_h3]:text-lg [&_h1]:font-semibold [&_h2]:font-semibold [&_h3]:font-semibold [&_h4]:font-semibold [&_h5]:font-semibold [&_h6]:font-semibold [&_a]:underline [&_a]:underline-offset-4 [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_pre]:overflow-x-auto [&_pre]:rounded-lg [&_pre]:border [&_pre]:bg-muted [&_pre]:p-4 [&_pre]:text-xs [&_code]:font-mono [&_:not(pre)>code]:rounded [&_:not(pre)>code]:bg-muted [&_:not(pre)>code]:px-1 [&_:not(pre)>code]:py-0.5 [&_blockquote]:border-l-2 [&_blockquote]:pl-4 [&_blockquote]:text-muted-foreground [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto [&_th]:border [&_th]:px-3 [&_th]:py-2 [&_th]:text-left [&_td]:border [&_td]:px-3 [&_td]:py-2">
        <Markdown remarkPlugins={[remarkGfm]} components={markdownComponents}>
          {item.text}
        </Markdown>
      </div>
      <div className="mt-2 flex items-center gap-2">
        <time className="text-xs text-muted-foreground" dateTime={item.time}>
          {new Date(item.time).toLocaleTimeString([], {
            hour: "2-digit",
            minute: "2-digit",
          })}
        </time>
        <div className="ml-auto">
          <CopyButton text={item.text} />
        </div>
      </div>
    </article>
  );
}
export function Chat() {
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
  const items = transcripts[agentId] || [];
  const [draft, setDraft] = useState(drafts.get(agentId) || "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const ready = !!agent && replayed[agentId];
  const queue = agent?.queue || [];
  const pending = queue.filter((m) => m.status === "pending");
  const running = queue.find((m) => m.status === "running");
  const context = agent?.context_usage;
  const contextPercent =
    context && context.context_window > 0
      ? Math.round((context.estimated_tokens / context.context_window) * 100)
      : undefined;
  const updateDraft = (text: string) => {
    draftVersions.set(agentId, (draftVersions.get(agentId) || 0) + 1);
    drafts.set(agentId, text);
    setDraft(text);
  };
  const action = async (fn: () => Promise<unknown>) => {
    if (inFlight.has(agentId)) return;
    inFlight.add(agentId);
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(String(e));
    } finally {
      inFlight.delete(agentId);
      setBusy(false);
    }
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const original = draft;
    if (!original.trim() || busy) return;
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
        updateDraft("");
        const version = draftVersions.get(agentId);
        try {
          await control.send(agentId, text, receipt.key);
          receipts.delete(agentId);
        } catch (error) {
          // Keep the retry key and restore only if the composer was untouched.
          if (draftVersions.get(agentId) === version) updateDraft(original);
          throw error;
        }
        return;
      }
      if (drafts.get(agentId) === original) {
        drafts.delete(agentId);
        setDraft("");
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
      <header className="flex h-14 shrink-0 items-center gap-3 border-b border-border px-4 md:px-6">
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
        {!ready ? (
          <Loading>Replaying chat history…</Loading>
        ) : (
          <MessageScrollerProvider autoScroll defaultScrollPosition="end">
            <MessageScroller>
              <MessageScrollerViewport>
                <MessageScrollerContent className="mx-auto max-w-3xl gap-0 px-5 py-8 md:px-8 [&>*+*]:mt-6 [&>[data-tool=true]+[data-tool=true]]:mt-1.5">
                  {!items.length && (
                    <p className="py-12 text-center text-sm text-muted-foreground">
                      Send a message to start this chat.
                    </p>
                  )}
                  {items.map((item) => (
                    <MessageScrollerItem
                      key={item.id}
                      messageId={item.id}
                      data-tool={
                        item.kind === "tool" || item.kind === "tool_result"
                      }
                    >
                      <Message item={item} />
                    </MessageScrollerItem>
                  ))}
                  {agent.state !== "idle" && (
                    <div
                      role="status"
                      className="flex items-center gap-2 py-2 text-sm leading-7 text-muted-foreground"
                    >
                      {agent.state === "stopping" ? (
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
          </MessageScrollerProvider>
        )}
        <div className="mx-auto w-full max-w-3xl shrink-0 space-y-3 px-4 pb-4 pt-2 md:px-8">
          <ErrorNotice error={error} />
          {notice && (
            <Alert>
              <AlertDescription>{notice}</AlertDescription>
              <Button size="xs" variant="ghost" onClick={() => setNotice(null)}>
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
            <div className="max-h-40 overflow-y-auto rounded-lg border border-border bg-muted px-3 py-2">
              <div className="flex items-center justify-between text-xs">
                <span className="font-medium">
                  {agent.held ? "Queue held" : "Up next"} · {pending.length}{" "}
                  pending
                </span>
              </div>
              {pending.map((m) => (
                <div
                  key={m.id}
                  className="mt-2 flex items-center gap-2 border-t border-border pt-2 text-xs"
                >
                  <span className="min-w-0 flex-1 truncate" title={m.text}>
                    {m.text}
                  </span>
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
          <form onSubmit={submit} aria-label="Message composer">
            <InputGroup
              aria-label="Message input"
              className="has-disabled:opacity-100 has-disabled:bg-transparent dark:has-disabled:bg-input/30"
            >
              <InputGroupTextarea
                autoFocus
                aria-label="Message"
                placeholder={
                  agent.settled
                    ? "Send a message to restore this chat…"
                    : "Message Ted…"
                }
                className="max-h-52 min-h-24"
                value={draft}
                onChange={(event) => updateDraft(event.target.value)}
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
              <InputGroupAddon
                align="block-end"
                className="items-end"
                aria-label="Composer toolbar"
              >
                <div
                  role="group"
                  aria-label="Chat settings"
                  className="flex min-w-0 flex-1 flex-wrap items-center gap-2"
                >
                  <ModelFields
                    compact
                    disabled={busy || !ready || status !== "live"}
                    model={agent.settings.model}
                    effort={agent.settings.effort}
                    onChange={(model, effort) =>
                      void action(() =>
                        control.settings(agentId, { model, effort }),
                      )
                    }
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label="View context"
                    title="View context usage and chat settings"
                    onClick={() => open("panel", "settings")}
                  >
                    Context
                    {contextPercent === undefined ? "" : ` ${contextPercent}%`}
                  </Button>
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
                      disabled={busy || !ready || status !== "live"}
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
                        void action(() => control.stopTurn(agentId, running.id))
                      }
                    >
                      <Square className="size-3 fill-current" />
                    </Button>
                  )}
                  <Button
                    type="submit"
                    size="icon"
                    disabled={
                      busy || !draft.trim() || !ready || status !== "live"
                    }
                    aria-label="Send message"
                  >
                    <ArrowUp />
                  </Button>
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
              </InputGroupAddon>
            </InputGroup>
          </form>
          {project && (
            <div
              role="group"
              aria-label="Project location"
              className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground"
            >
              <span
                className="min-w-0 max-w-full truncate"
                title={project.root}
              >
                {project.root}
              </span>
              {project.git_branch && (
                <span
                  className="flex min-w-0 max-w-full items-center gap-1"
                  title={project.git_branch}
                >
                  <GitBranch className="size-3 shrink-0" aria-hidden="true" />
                  <span className="truncate">{project.git_branch}</span>
                </span>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
