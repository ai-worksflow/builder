# Repository Guidelines

## Project Structure & Module Organization

This repository is organized around a Next.js frontend in `frontend/`. App Router entry points live in `frontend/app/`, shared UI components live in `frontend/components/`, and reusable logic lives in `frontend/lib/`. The Worksflow feature is grouped under `frontend/components/worksflow/` and `frontend/lib/worksflow/`; keep new feature-specific code near those folders unless it is broadly reusable. Static assets are stored in `frontend/public/`. Product and workflow notes live in `docs/`.

## Build, Test, and Development Commands

Run commands from `frontend/` unless noted otherwise.

```sh
pnpm install
pnpm dev
pnpm build
pnpm start
pnpm lint
```

`pnpm install` restores dependencies from `pnpm-lock.yaml`. `pnpm dev` starts the local Next.js development server. `pnpm build` creates a production build, and `pnpm start` serves that build. `pnpm lint` runs ESLint across the frontend source tree.

## Coding Style & Naming Conventions

Use TypeScript and React function components. The project uses strict TypeScript settings and the `@/*` path alias for imports from the `frontend/` root. Match the existing style: two-space indentation, single quotes, no semicolons, and descriptive kebab-case filenames such as `prompt-composer.tsx`. Use PascalCase for exported React components and camelCase for hooks, helpers, variables, and functions. Prefer shared utilities from `frontend/lib/` and existing UI primitives from `frontend/components/ui/` before adding new patterns.

## Testing Guidelines

No automated test runner is configured yet. For now, validate changes with `pnpm lint` and, for UI changes, manual checks in `pnpm dev`. When adding tests, colocate them near the code they cover with names like `component-name.test.tsx` or place broader integration tests under a clearly named test directory. Add the test command to `frontend/package.json` in the same change that introduces the framework.

## Commit & Pull Request Guidelines

The current history only contains a minimal `first commit`, so use concise, imperative commit messages such as `Add workflow preview panel` or `Fix locale label fallback`. Pull requests should include a short description, validation steps, and screenshots or recordings for visible UI changes. Link related issues or docs when applicable, and call out any follow-up work or configuration changes.

## Security & Configuration Tips

Do not commit secrets, local environment files, build output, or dependency directories. Keep generated files such as `.next/`, `node_modules/`, `.DS_Store`, and `tsconfig.tsbuildinfo` out of reviews unless a maintainer explicitly requests them.
