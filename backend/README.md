# Worksflow backend

The Go system of record for the Worksflow collaborative application-generation
platform. It serves authentication and project RBAC, versioned artifacts and
reviews, the governed conversation control plane, typed workflow execution,
Workbench generation, application data, quality and delivery, durable audit /
outbox events, and authenticated realtime subscriptions.

## Local commands

```sh
cp .env.example .env
set -a
. ./.env
set +a
make run
```

Run verification with `make check`. Start the complete local stack from the
repository root with `docker compose up --build`.

## Runtime endpoints

- `GET /health/live`: process liveness
- `GET /health/ready`: PostgreSQL, Redis, MongoDB, and NATS JetStream readiness
- `POST /v1/session/register`, `POST /v1/session`, `GET/DELETE /v1/session`
- `GET/POST/PATCH/DELETE /v1/projects...`: project, member, and invitation APIs
- `/v1/projects/:projectId/conversations...`: immutable messages, reviewed AI
  intent proposals, and controlled workflow / Workbench commands
- `/v1/projects/:projectId/artifacts...` and `/workflow-*`: versioned product
  facts, immutable manifests, typed definitions, and durable runs
- `GET /v1/ws`: authenticated project/artifact/run subscriptions

Browser sessions use an HttpOnly cookie and a separate CSRF cookie/header. The
default WebSocket authenticator uses the same session. `WS_ALLOW_ANONYMOUS=true`
is an explicit development-only escape hatch and is rejected in production.
