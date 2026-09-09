import { expect, it } from "vitest";
import { cn, toolCommand } from "./utils";

it("unwraps historical command notifications without changing raw commands", () => {
  expect(toolCommand('Ran shell command - "echo one\\necho two"')).toBe(
    "echo one\necho two",
  );
  expect(toolCommand('Ran shell command - "echo \\"hello\\""')).toBe(
    'echo "hello"',
  );
  expect(toolCommand("ls -la")).toBe("ls -la");
});

it("named grid spacing respects component overrides", () => {
  expect(cn("p-inset", "p-0")).toBe("p-0");
  expect(cn("p-4", "p-inset")).toBe("p-inset");
  expect(cn("md:px-inset", "md:px-4")).toBe("md:px-4");
  expect(cn("max-w-chat", "max-w-none")).toBe("max-w-none");
});
