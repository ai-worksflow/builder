---
name: frontend-resource-graph
description: Derive and implement a production frontend resource graph from exact product requirements. Use for UI, page, component, or frontend content tasks that need icons, images, illustrations, logos, fonts, video, or other visual assets; also use when reviewing whether a frontend implementation improperly substitutes emoji, Unicode glyphs, placeholders, or unapproved remote assets for real resources.
---

# Frontend Resource Graph

Build the visual asset layer as part of the implementation, not as unspecified follow-up work. Keep every resource traceable to a requirement and a concrete UI consumer.

## Derive the graph

1. Read the exact BuildContract, bound acceptance criteria, routes, states, and source material.
2. Inspect the repository before choosing assets: existing asset folders, package dependencies, icon libraries, component primitives, design tokens, framework image components, and established naming conventions.
3. Build a private graph with one node per required resource. Record its stable ID, kind, purpose, requirement IDs, consuming route/component/state, source strategy, output path or library symbol, variants, accessibility treatment, and status.
4. Add dependency edges from requirements to resources and from resources to every UI consumer. Add `variant_of` edges for responsive, theme, locale, or state variants.
5. Implement every resolvable node and report the exact graph in the structured result. Do not create an extra manifest file unless the TaskCapsule write set and repository conventions require one.

## Choose sources

- Reuse exact supplied or existing repository assets when they satisfy the requirement.
- Use the project's established icon library for interface icons. Match its stroke, size, alignment, and accessible-label conventions.
- Use CSS primitives for simple separators, status dots, skeletons, and geometry that are not content assets.
- Generate a local SVG only for a bespoke illustration, diagram, pattern, or other scalable visual that is not an interface icon and does not invent protected brand identity. Keep SVG markup deterministic, accessible, responsive, and free of embedded scripts or remote references.
- Generate raster or photographic assets only when an approved image-generation tool is actually available and the result can be written into the declared write set. Otherwise report the exact missing asset as a blocker.
- Treat logos, trademarks, product screenshots, and brand photography as authoritative inputs. Never fabricate them.

## Prohibited substitutes

- Never use emoji, emoticons, Unicode dingbats, miscellaneous symbols, letters, or punctuation as interface icons.
- Never use a text label such as `image`, `logo`, `TODO`, or `placeholder` in place of a required visual resource.
- Never hotlink remote images, pull arbitrary stock assets, or add a new icon package when an established project resource already fits.
- Never draw ad hoc SVG interface icons when the established icon library provides the semantic icon.
- Never claim an asset is generated, reused, or verified unless the exact file or library symbol exists in the final worktree.

## Implement to frontend standards

- Import icons as components; keep decorative icons hidden from assistive technology and give icon-only controls an accessible name and visible focus state.
- Give informative images meaningful alternative text. Use empty alternative text for decorative images.
- Declare intrinsic dimensions or aspect ratio, provide responsive variants when required, and use the framework's image optimization path when the repository already uses it.
- Preserve design tokens, theme behavior, localization, reduced-motion preferences, loading/error/empty states, and responsive layouts.
- Keep resource filenames stable, descriptive, lowercase, and compatible with repository conventions. Avoid duplicate near-identical assets.
- Run the declared verification commands and inspect the final changed paths before reporting completion.

## Report closure

Mark the resource graph applicable whenever a bound requirement or implemented UI consumes a visual asset. List every node once in stable ID order and every edge once in stable `(from, to, relation)` order. A missing required node, unavailable approved generation tool, unresolved brand input, unverified path, or prohibited substitute is a blocker; do not report the TaskCapsule complete.
