import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Historical tool notifications wrap Go-quoted commands in a prose prefix.
export function toolCommand(text: string): string {
  const prefix = "Ran shell command - ";
  if (!text.startsWith(prefix)) return text;
  const quoted = text.slice(prefix.length);
  try {
    const command: unknown = JSON.parse(quoted);
    if (typeof command === "string") return command;
  } catch {
    // Go may use escapes that JSON does not support. Keep those readable.
  }
  return quoted.startsWith('"') && quoted.endsWith('"')
    ? quoted.slice(1, -1)
    : quoted;
}
