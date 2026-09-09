import { expect, it } from "vitest";

// Source guard for numeric layout spacing, not typography, icons or geometry.
// Keep exceptions small and explicit; measured browser tests validate real layout.
const components = import.meta.glob("../**/*.tsx", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

it("uses the 8px spacing grid in all web components", () => {
  const exceptions: Record<string, string[]> = {
    "../components/chat.tsx": ["px-1", "py-0.5"], // inline code optical padding
    "../components/ui/badge.tsx": ["py-0.5"], // fixed-height noninteractive badge
  };
  const violations: string[] = [];
  for (const [file, source] of Object.entries(components)) {
    const tokens = source.matchAll(
      /(?<![\w-])((?:p[xytrblse]?|m[xytrblse]?|gap(?:-[xy])?|space-[xy])-(\d+(?:\.\d+)?))(?![\w.-])/g,
    );
    for (const [, token, value] of tokens) {
      if (Number(value) % 2 !== 0 && !exceptions[file]?.includes(token)) {
        violations.push(`${file}: ${token}`);
      }
    }
  }
  expect(Object.keys(components).length).toBeGreaterThan(20);
  expect(violations).toEqual([]);
});
