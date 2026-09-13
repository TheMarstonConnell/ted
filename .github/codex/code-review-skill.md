<!-- Vendored from https://github.com/mattpocock/skills (code-review skill),
for use by .github/workflows/codex-review.yml. Frontmatter was removed and the
prose lightly rewritten; the two-axis process is unchanged. -->

# Two-axis code review

Review the diff between `HEAD` and a supplied fixed point on two independent
axes:

- **Standards.** Does the code follow this repository's documented standards?
- **Spec.** Does the code do what the originating issue or spec asked?

The reports stay separate so one axis cannot mask the other.

## 1. Pin the fixed point

Use the fixed point supplied by the caller. Capture the diff with
`git diff <fixed-point>...HEAD`; the three dots intentionally diff against the
merge base. Capture the commit list with
`git log <fixed-point>..HEAD --oneline`.

Confirm the ref resolves with `git rev-parse <fixed-point>` and that the diff
is non-empty before reviewing.

## 2. Identify the spec

Look for a specification in this order:

1. Issues referenced by the PR or its commits.
2. A path supplied by the caller.
3. A current file under `docs/`, `specs/`, or `.scratch/` that the branch or
   feature clearly identifies.
4. If none exists, report "no spec available" for the Spec axis.

## 3. Identify the standards

Read files that document how code in the changed area should be written. In
addition, use the Fowler-style smell baseline below. Two rules govern it:

- A documented repository standard overrides the baseline.
- Smells are judgment-call heuristics, never hard violations. Skip concerns
  already enforced by tooling.

Check the diff for these smells:

- **Mysterious Name.** A function, variable, or type name does not reveal what
  it does or holds. Rename it; if no honest name fits, clarify the design.
- **Duplicated Code.** The same logic shape appears in multiple changed places.
  Extract and reuse the shared shape.
- **Feature Envy.** A method reaches into another object's data more than its
  own. Move the behavior to the data it uses.
- **Data Clumps.** The same fields or parameters repeatedly travel together.
  Bundle the concept into a type.
- **Primitive Obsession.** A primitive or string represents a domain concept
  that needs its own small type.
- **Repeated Switches.** The same switch or conditional cascade on one type
  recurs. Centralize the mapping or use polymorphism.
- **Shotgun Surgery.** One logical change forces scattered edits across many
  files. Gather the behavior into one module.
- **Divergent Change.** One file is edited for several unrelated reasons.
  Split responsibilities.
- **Speculative Generality.** An abstraction, parameter, or hook serves needs
  absent from the spec. Remove or inline it until a real need exists.
- **Message Chains.** Long navigation such as `a.b().c().d()` exposes structure
  the caller should not know. Hide the walk behind behavior.
- **Middle Man.** A type or function mostly delegates. Remove the layer and
  call the real target directly.
- **Refused Bequest.** A subtype ignores most of what it inherits. Prefer
  composition over that inheritance.

## 4. Review both axes independently

For **Standards**, report per file or hunk:

- Every documented-standard violation, citing the file and rule.
- Any baseline smell, naming it and quoting the relevant hunk.
- Whether each concern is a hard violation or judgment call.

For **Spec**, report:

- Missing or partial requirements.
- Behavior the spec did not request (scope creep).
- Requirements that appear implemented but whose implementation is wrong.
- The relevant spec language for each finding.

## 5. Aggregate without reranking

Present the reports under `## Standards` and `## Spec`. Do not merge or rerank
their findings. End with one line giving the finding count and worst issue for
each axis, if any.

A change can follow every standard while implementing the wrong behavior, or
implement the requested behavior while violating repository conventions.
Keeping the axes separate makes both failures visible.
