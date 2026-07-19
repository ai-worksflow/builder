# Worksflow sandbox runner

This image is the fixed, non-root execution substrate for an interactive
`SandboxSession`. It does not contain project code, credentials, a Docker
socket, or platform control-plane access.

Builds must supply both inputs explicitly:

```sh
docker build \
  --build-arg GO_IMAGE='golang@sha256:<approved-build-base-digest>' \
  --build-arg NODE_IMAGE='node@sha256:<approved-base-digest>' \
  --build-arg CODEX_VERSION='<tested-version>' \
  --tag worksflow/sandbox-runner:<version> \
  sandbox-runner
```

Admission records the resulting OCI digest. The API accepts only that digest,
pre-provisions it, and never pulls an image while handling a user request.
The same image supplies a fixed TCP gateway executable. Project code runs only
on a per-session internal network; a separate mount-free, secret-free gateway
container joins that network and publishes only manifest-allowed ports.
Interactive terminals are opened only through the fixed
`worksflow-sandbox-pty` helper. The helper allocates a non-root `/bin/bash`
PTY inside `/workspace`, rejects traversal and symlink working directories,
and accepts only bounded input, resize, signal, and close packets. Browser
clients never receive a container runtime socket or an arbitrary `docker exec`
surface.
Codex credentials are injected only into one `codex exec` process by the
future secret broker; they are never placed in the long-lived container
environment or workspace.
