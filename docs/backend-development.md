# Backend development

The Go backend lives in `backend/` and uses module
`github.com/worksflow/builder/backend`. The current layer is infrastructure
only: Gin HTTP serving, operational middleware, health probes, persistence and
messaging clients, graceful shutdown, and a WebSocket hub.

## Start the local stack

From the repository root:

```sh
docker compose up --build
```

This starts PostgreSQL, Redis, MongoDB, NATS with JetStream, and the API. The
local credentials in `docker-compose.yml` are development-only values.

Verify the process and dependency health:

```sh
curl -s http://localhost:8080/health/live
curl -s http://localhost:8080/health/ready
```

Stop the stack without deleting persistent volumes:

```sh
docker compose down
```

Add `--volumes` only when deliberately resetting local infrastructure data.

## Run the API outside Docker

Start the four dependencies with Compose, copy `backend/.env.example` to an
ignored `.env`, export it in the shell, and run:

```sh
cd backend
make run
```

Configuration is environment-driven and validated before infrastructure is
opened. Invalid ports, durations, URLs, connection pool limits, wildcard CORS
with credentials, or unsafe production CORS fail startup.

## Delivery quality isolation

Dependency-aware quality runs use two separate container stages:

1. The resolver stage receives only `package.json` plus
   `package-lock.json`, or `go.mod` plus `go.sum`. It runs on the explicitly
   configured resolver network with separate timeout, output, memory, CPU and
   PID limits. npm integrity and registry origins are checked before
   `npm ci --ignore-scripts`; Go uses one HTTPS `GOPROXY`, a fixed `GOSUMDB`,
   and rejects local `replace` directives.
2. Build, type, lint and test stages receive the frozen source and the
   read-only prepared dependency directory. Their container network is always
   `none`.

When `DELIVERY_SANDBOX_HOST` is a TCP/DinD daemon,
`DELIVERY_QUALITY_TEMP_ROOT` must be an absolute directory mounted at the same
path in both the API and daemon containers. The Compose stack provides the
shared `quality-workspaces` volume for this boundary.

A passing run captures `dist/`, `out/`, `build/`, or a root static
`index.html` as an immutable MongoDB content object. Preview, production and
rollback deploy that exact build artifact; they never rebuild or pass the
source workspace to the provider.

## Verification

```sh
cd backend
make check
make test-race
```

`make check` verifies formatting, runs `go vet ./...`, and runs `go test ./...`.

## WebSocket authentication boundary

`GET /v1/ws` authenticates with the same session service as HTTP. Cookie-backed
browser connections must include the CSRF token in the first `auth` message;
Bearer tokens are accepted for WebSocket and non-browser clients. After an
`auth.ack`, clients can subscribe to authorized project, artifact, or workflow
run scopes. NATS JetStream events are fanned out with a stream-sequence cursor.

The live fan-out consumer starts at new events. A stale client cursor receives
`cursor.reset`; durable per-client historical replay remains a later extension.

## Operational behavior

- Logs are structured JSON by default and include request ID, status, latency,
  response size, method, path, and client IP.
- `X-Request-ID` is preserved when valid or generated otherwise.
- Panic recovery returns a generic error and records a structured stack trace.
- Security headers and explicit CORS policy apply to every route.
- Readiness concurrently checks PostgreSQL, Redis, MongoDB, and JetStream with
  per-check deadlines and does not expose raw infrastructure errors.
- SIGINT/SIGTERM stops accepting HTTP work, closes WebSockets, drains NATS, and
  closes MongoDB, Redis, and PostgreSQL within the configured shutdown window.
- Development startup applies checksum-protected, advisory-locked migrations,
  creates Mongo indexes and the NATS stream, and starts the transactional outbox.
  Production defaults these mutations off and requires explicit provisioning or
  explicit opt-in.

No schema migrations or streams are created by this foundation. Domain owners
must add them through explicit, versioned startup or deployment workflows.
