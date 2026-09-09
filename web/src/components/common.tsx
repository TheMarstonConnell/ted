import { useId, type ReactNode } from "react";
import { AlertCircle, LoaderCircle } from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { useControl } from "@/lib/store";

export function ErrorNotice({ error }: { error?: string | null }) {
  if (!error) return null;
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertDescription className="min-w-0 [overflow-wrap:anywhere]">
        {error}
      </AlertDescription>
    </Alert>
  );
}
export function Loading({ children = "Loading…" }: { children?: ReactNode }) {
  return (
    <div
      role="status"
      className="flex items-center justify-center gap-2 p-8 text-sm text-muted-foreground"
    >
      <LoaderCircle className="size-4 animate-spin" />
      {children}
    </div>
  );
}
export function ModelFields({
  model,
  effort,
  onChange,
  compact = false,
  disabled = false,
}: {
  model: string;
  effort: string;
  onChange: (model: string, effort: string) => void;
  compact?: boolean;
  disabled?: boolean;
}) {
  const { models } = useControl();
  const modelId = useId();
  const effortId = useId();
  const selected = models.find((m) => m.id === model);
  const modelItems = models.map((m) => ({ value: m.id, label: m.name }));
  if (!selected)
    modelItems.unshift({ value: model, label: model || "Select a model" });
  const effortItems = selected?.efforts.length
    ? selected.efforts.map((e) => ({ value: e, label: e || "Default" }))
    : [{ value: effort, label: compact ? "N/A" : "Not configurable" }];
  return (
    <div className={compact ? "contents" : "space-y-4"}>
      <div className="grid min-w-0 max-w-full grid-cols-[minmax(0,1fr)] gap-2">
        <Label htmlFor={modelId} className={compact ? "sr-only" : undefined}>
          Model
        </Label>
        <Select
          items={modelItems}
          value={model}
          disabled={disabled || !models.length}
          onValueChange={(value) => {
            const next = models.find((m) => m.id === value);
            if (next)
              onChange(
                next.id,
                next.efforts.includes(effort) ? effort : next.default_effort,
              );
          }}
        >
          <SelectTrigger
            id={modelId}
            size={compact ? "sm" : "default"}
            title={compact ? `Model: ${selected?.name || model}` : undefined}
            className={compact ? "min-w-0 max-w-full" : "w-full"}
          >
            <SelectValue className="min-w-0 truncate" />
          </SelectTrigger>
          <SelectContent
            side={compact ? "top" : "bottom"}
            align="start"
            alignItemWithTrigger={false}
            className="w-max max-w-[calc(100vw-2rem)]"
          >
            {!selected && (
              <SelectItem value={model}>{model || "Select a model"}</SelectItem>
            )}
            {[...new Set(models.map((m) => m.provider))].map((provider) => (
              <SelectGroup key={provider}>
                <SelectLabel>{provider}</SelectLabel>
                {models
                  .filter((m) => m.provider === provider)
                  .map((m) => (
                    <SelectItem
                      key={m.id}
                      value={m.id}
                      className="[&>span]:min-w-0"
                    >
                      <span className="truncate" title={m.name}>
                        {m.name}
                      </span>
                    </SelectItem>
                  ))}
              </SelectGroup>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="grid min-w-0 max-w-full grid-cols-[minmax(0,1fr)] gap-2">
        <Label htmlFor={effortId} className={compact ? "sr-only" : undefined}>
          Reasoning effort
        </Label>
        <Select
          items={effortItems}
          value={effort}
          disabled={disabled || !selected?.efforts.length}
          onValueChange={(value) => {
            if (value !== null) onChange(model, value);
          }}
        >
          <SelectTrigger
            id={effortId}
            size={compact ? "sm" : "default"}
            title={
              compact
                ? `Reasoning effort: ${selected?.efforts.length ? effort || "Default" : "Not configurable"}`
                : undefined
            }
            className={compact ? "min-w-0 max-w-full" : "w-full"}
          >
            <SelectValue className="min-w-0 truncate" />
          </SelectTrigger>
          <SelectContent
            side={compact ? "top" : "bottom"}
            align="start"
            alignItemWithTrigger={false}
          >
            <SelectGroup>
              {effortItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      {!models.length && (
        <p className="order-last col-span-full basis-full text-sm text-muted-foreground">
          No models available. Configure provider credentials on the server,
          then{" "}
          <Button
            variant="link"
            className="h-auto min-h-0 min-w-0 p-0 align-baseline"
            onClick={() => location.reload()}
          >
            reload
          </Button>
          .
        </p>
      )}
    </div>
  );
}
