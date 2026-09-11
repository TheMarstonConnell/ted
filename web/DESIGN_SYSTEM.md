# Web interface grid

Source: [Marvin Schwaibold's layered grid guidelines](https://x.com/MSchwaibold/status/2096306405649318139).
Ted adopts the constraints, not the example's font pairing or a new information
architecture. All web states are in scope; the terminal UI is independent.

## Layers

1. **Layout:** an 8px grid with shared column boundaries.
2. **Safe space:** 24px major insets; 16px nested/mobile insets; 8px controls.
3. **Grouping:** related content stays together; separate tasks get more space.
4. **Hierarchy:** at most three sizes and weights per component (exceptions below).

The source of truth is `src/index.css`, the shared `components/ui` primitives,
and the regression tests—not individual pixel corrections on each screen.

## Spacing and layout

| Role | Value | Tailwind |
| --- | --- | --- |
| Adjacent controls, label/field, consecutive tool entries | 8px | `gap-2`, `space-y-2` |
| Nested card padding, paragraph boundaries, mobile gutters | 16px | `p-4`, `mt-4` |
| Major surface inset, form sections, message boundaries | 24px | `p-inset`, `gap-6`, `mt-6` |
| Desktop transcript/composer gutter | 32px | `md:px-8` |
| Workspace header | 56px | `h-14` |
| Sidebar | 256px | `w-64` |
| Chat container including gutters | max 768px | `max-w-chat` |

Use 0/8/16/24/32/40/48/56/64px for layout spacing. Do not change Tailwind's
underlying 4px unit: that would silently double existing sizes and icon offsets.
Do not use `gap-3`, `p-2.5`, or similar intermediate values to fix crowded groups.
Fix the grouping, wrapping, or hierarchy instead.

Titles and their secondary metadata form one typographic group (no added margin
between lines). Chat paragraphs have 16px boundaries; individual lines do not
gain extra margins.
User and assistant messages contain only their body, with no timestamp, copy
button, or reserved footer space. Message boundaries still use 24px spacing.

Transcript and composer reserve the same thin native scrollbar gutters on both
sides. Keep their outer containers and horizontal padding synchronized. The chat
column remains centered on wide screens. Code and tables scroll within it; long
prose wraps. Images preserve their aspect ratio.

## Surfaces and controls

| Role | Radius | Insets |
| --- | --- | --- |
| Composer, dialogs, overview cards, mobile drawer top | 24px (`rounded-xl`) | 24px major surfaces; composer 16px on mobile |
| User bubble | 24px | 16px (compact content surface) |
| Tool cards, code blocks, menus, images | 16px (`rounded-lg`) | 16px cards; 8px menus |
| Buttons, inputs, selects, menu items | 8px (`rounded-md`) | 8px compact / 16px normal |
| Inline code / tiny noninteractive details | 4px (`rounded-sm`) | Optical exception |

Default desktop buttons and inputs are 40px tall. Compact buttons/selects are
32px; large controls are 48px. Icon controls use square variants. Text inputs use
16px type on mobile to avoid iOS focus zoom. Narrow screens and coarse pointers
get **at least 48px control targets**, including sidebar actions, menu items, and composed collapsible/dialog triggers.
Do not reduce those targets to force controls onto a single line.

The composer field starts at 64px on desktop and 48px on mobile, grows to 208px,
then scrolls. On narrow screens, cap growth at the smaller of 208px and 25dvh
(the 48px minimum still applies) so short viewports retain space for controls. Its bottom toolbar wraps model/effort controls independently from
the non-shrinking send/stop/continue group. The context/navigation footer remains
outside the input, but shares its text-area insets (16px mobile / 24px desktop,
plus a matching transparent 1px border for exact alignment). Context is read-only
12px muted IBM Plex Mono text, like the path and branch; it has no button styling,
focus stop, or click action. Model/effort changes use the composer toolbar;
there is no separate agent settings dialog. Project defaults remain in the sidebar and affect new chats
only. Legacy agent `?panel=settings` links show ordinary chat without a modal.
Dialogs scroll within the viewport and reserve title space for the close control; destructive actions wrap on narrow screens.

## Scroll intent

Being geometrically at the bottom does not necessarily mean the scroller is
following: a horizontal wheel or touch gesture can pause that mode without a
vertical scroll. A successful local send must explicitly call the scroller's
`scrollToEnd({ behavior: "auto" })` to resume following through later content and
composer-size changes, including a queued turn that starts after acknowledgement.

Keep the provider scoped to a chat and shared by its composer and transcript.
Do not scroll on failed requests or slash commands. If the user interacts with
the transcript while a send is pending, preserve their newer reading intent
instead of forcing a jump when the request completes. A late response from an
unmounted chat must never scroll the current chat. Normal incoming output still
respects reading position; the existing manual jump-to-latest control is unchanged.
Use the scroller API rather than arbitrary delays, direct `scrollTop` writes, or
remounting the transcript. Typing must still leave memoized message bodies alone.

## Tool and sidebar hierarchy

- Tool calls with no returned output show a 16px spinner in the chevron's place.
  The trigger is disabled and marked busy until a result arrives; do not offer an
  empty dropdown. Honor reduced motion with a static loading icon.
- A returned result switches the spinner to the normal expand chevron without
  changing row height or auto-expanding. Empty-string results count as complete
  and expand to “No output returned.” This is output availability, not a claim
  that execution succeeded. Unmatched historical results remain expandable.
- Selected chat rows use the neutral primary fill, contrasting text, a semibold
  title, and secondary branch text at 80% foreground opacity. Preserve that
  contrast even when the selected chat is settled. Other titles use medium weight.
- Project headings use a folder icon, semibold title, and trailing chevron.
  Project settings and chat settle/restore share a row-scoped reveal: 0px width
  on fine-pointer desktop until hover/focus, then 32px plus an 8px gap. On touch
  devices the action remains visible with a 48px target. Keep hidden actions in
  keyboard order, and do not reveal a parent action when focusing a child row.
  Running/Held/Stopping stay plain text with sufficient emphasis to scan. Do not
  add duplicate state badges or compress tap targets to fit long titles.

## Typography

Fonts are bundled locally through Fontsource, with `font-display: swap`, normal
and italic faces, and licenses shipped in `dist/fonts`. No runtime font CDN.

- **Inter:** prose, headings, navigation, labels, buttons, and ordinary UI data.
- **IBM Plex Mono:** code, shell output/commands, file paths, branch values,
  keyboard shortcuts, and the footer context readout. Natural-language fallbacks may inherit mono when they
  occupy a path/branch slot.
- Weights: **400 / 500 / 600**. Strong text uses 600, not an extra 700 weight.
- Metadata: **12px / 16px**; expanded code/output: **12px / 20px**.
- UI labels and body: **14px / 20px**.
- Transcript and working/stopping status: **14px / 24px**.
- Dialog and major Markdown headings: **20px / 28px**.
- Input accessibility size: **16px / 24px** on mobile.

Markdown uses three sizes: 12px code, 14px prose and lower headings, 20px H1/H2.
Preserve semantic heading levels even when they share a visual size. New
components should use no more than three sizes and three weights; do not add
one-off type sizes for individual labels.

## Explicit exceptions

- Font sizes/line heights are typographic, not forced to multiples of 8.
- Mobile text-entry fields may add a fourth size within a dialog to prevent zoom.
- 1px borders, 2px blockquote rules, focus rings, SVG/icon dimensions, inline-code
  padding, badge padding, and small icon optical offsets are not layout gaps.
- Inline prose links (including the model-unavailable reload action) retain
  text-sized targets rather than becoming 48px blocks inside sentences.
- Native scrollbar thickness, viewport safe areas, percentage/fluid widths,
  image aspect ratios, animation transforms, and text-driven heights are not
  grid-rounded.
- The image lightbox deliberately overrides dialog padding/radius for a
  near-full-window image; it is not a 24px padded card.

Keep the neutral light/dark theme, accessible labels, keyboard selection/focus
restoration, and reduced-motion behavior. Do not introduce decorative surfaces
just to demonstrate the grid.

## Maintenance and verification

- Change shared primitives first, then adjust content groupings.
- Register named spacing/container tokens with `extendTailwindMerge` in
  `lib/utils.ts` so local overrides actually replace defaults.
- `npm test` guards numeric component spacing against off-grid additions, checks
  named token overrides, and tests application state logic.
- `npm run test:e2e` checks workflow behavior plus measured grid constraints at
  320/390/768/1440/1920px in light and dark themes. Grid tests attach chat and
  project-default screenshots. Existing tests cover queues, tools, images, drawer
  hand-offs, long content/catalogs, keyboard use, and reduced motion.
- For visual evidence, run `TED_WEB_RECORD=1 npx playwright test grid.spec.ts`.
- Run `npm run build` and include regenerated `web/dist` with source changes.

The focused `npm run test:iphone` suite uses WebKit with iPhone emulation and
checks selected-row/branch contrast (at least 4.5:1), spinner/result transitions,
tap targets, short viewport layout, and modal focus restoration. Chromium runs
the same scenarios in the regular suite. Native iOS keyboard and browser-toolbar
behavior still require a real-device check; shrinking a test viewport is not a
substitute for opening the software keyboard.

## Running agents

Only unsettled, unheld agents whose state is `running` get the sidebar's bold
shimmering Running label and eight-second border beam. Both use the row's text
color, preserving selected-row contrast in light and dark themes without changing
row geometry or covering the separate settle action. Reduced motion uses a static
border and solid text; forced-color mode uses a system-colored solid border.
