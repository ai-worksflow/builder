# Worksflow Builder Prototype

Worksflow Builder is a Next.js prototype for validating an AI generation workbench and the adjacent team collaboration domain. The current implementation focuses on the logged-in product surface: planning, building, previewing generated output, linking team documents, editing a flexible document graph, authoring blueprints, and managing prototype/import/review flows.

The main product specification is in `docs/worksflow-generation-workbench-prototype-spec.md`.

## Current Scope

- Workbench flow: Planning -> Plan Ready -> Building -> Complete.
- Preview, Code, and Database workspace modes.
- Project title menu, more menu, linked document picker, publish/share/connect demo panels.
- Team Collaboration dashboard with project switching.
- Document Graph with draggable nodes, relation filters, free binding, member binding, and Workbench context handoff.
- Document Editor with status changes, members, comments, history, downstream generation, and Workbench use.
- Blueprint Editor with module library, draggable nodes, graph edges, validation, generated docs, and Workbench context creation.
- Prototype Studio with wireframe/design/component/handoff modes, layer editing, states, fixtures, and export-style actions.
- Design Import Center and Review Center mock flows.
- Chinese and English UI copy through the local i18n provider.

## Mock Boundaries

This repository is still a high-fidelity prototype. The following capabilities intentionally use local mock state instead of external services:

- AI plan/build generation and task progress.
- Generated source files and terminal output in Code view.
- Database creation and Supabase connection.
- GitHub connection, publish, share, transfer, export, analytics, knowledge, and connector actions.
- Figma, Penpot, Excalidraw, tldraw, Storybook, and upload integrations.
- Review comments, notifications, permissions, and audit history.
- Persistence across browser refreshes beyond locale preference.

When productizing, move these mock actions behind typed service boundaries before adding real credentials, OAuth, webhooks, or storage.

## Project Structure

```text
frontend/
  app/                         Next.js App Router entry
  components/worksflow/        Product prototype UI
  components/worksflow/team/   Team collaboration surfaces
  components/worksflow/workbench/
  lib/i18n/                    Locale config, provider, messages
  lib/worksflow/               Types, mock data, project model, store
  tests/                       Model and flow tests
docs/
  worksflow-generation-workbench-prototype-spec.md
```

## Development

Run commands from `frontend/`.

```sh
pnpm install
pnpm dev
pnpm lint
pnpm typecheck
pnpm test:mock
pnpm test:e2e
pnpm build
```

`pnpm test` runs both mock model tests and Playwright interaction tests.

## Manual Demo Path

1. Open the Workbench.
2. Wait for Planning to advance to Plan Ready.
3. Click `Implement this plan`.
4. Watch the checklist advance to Complete.
5. Switch between Preview, Code, and Database.
6. Open Linked docs, then jump to the Team Document Graph.
7. Use a document or Blueprint selection in Workbench.
8. Sync the completed Workbench result back to team documents.

## Quality Gates

Before considering a change complete, run:

```sh
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

The production build now performs TypeScript validation. Do not re-enable `ignoreBuildErrors`.

## Next Productization Work

- Continue splitting the large store into Workbench, Team, Blueprint, Import, and Prototype domains. Route parsing/path generation already lives in `frontend/lib/worksflow/route-state.ts`.
- Continue splitting Prototype Studio and Blueprint Editor into smaller canvas/tool/inspector components. Shared Prototype Studio helpers already live outside the main component.
- Add URL-backed state for deep links and refresh recovery.
- Replace mock integrations with typed service adapters.
- Extend the data model with document versions, approved snapshots, audit metadata, and external prototype artifacts.
- Add broader browser coverage for drag/drop, responsive layouts, keyboard behavior, and accessibility.
