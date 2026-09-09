import { expect, it } from "vitest";
import { toolCommand } from "./utils";

it("unwraps historical command notifications without changing raw commands", () => {
  expect(toolCommand('Ran shell command - "echo one\\necho two"')).toBe(
    "echo one\necho two",
  );
  expect(toolCommand('Ran shell command - "echo \\"hello\\""')).toBe(
    'echo "hello"',
  );
  expect(toolCommand("ls -la")).toBe("ls -la");
});
