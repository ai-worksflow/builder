# Worksflow backend

Infrastructure foundation for the Worksflow API. It intentionally contains no
business-domain migrations, handlers, or message subjects.

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
- `GET /v1/ws`: authenticated project/artifact/run subscriptions

Browser sessions use an HttpOnly cookie and a separate CSRF cookie/header. The
default WebSocket authenticator uses the same session. `WS_ALLOW_ANONYMOUS=true`
is an explicit development-only escape hatch and is rejected in production.
