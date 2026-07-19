# All build arguments are intentionally required. CI/admission must supply
# digest-pinned build/runtime bases and a tested Codex CLI version; a mutable
# implicit default would make the runtime image impossible to reproduce.
ARG GO_IMAGE
ARG NODE_IMAGE
ARG CODEX_VERSION
ARG CODEX_INTEGRITY

FROM ${GO_IMAGE} AS gateway-build
ARG GO_IMAGE
ARG NODE_IMAGE
ARG CODEX_VERSION
ARG CODEX_INTEGRITY

WORKDIR /src
COPY validate-runner-build-args.sh /usr/local/libexec/validate-runner-build-args.sh
RUN sh /usr/local/libexec/validate-runner-build-args.sh "${GO_IMAGE}" "${NODE_IMAGE}" "${CODEX_VERSION}" "${CODEX_INTEGRITY}"
COPY gateway/main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/worksflow-sandbox-gateway ./main.go

WORKDIR /process
COPY process/main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/worksflow-sandbox-process ./main.go

WORKDIR /pty
COPY pty/go.mod pty/go.sum ./
RUN go mod download
COPY pty/main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/worksflow-sandbox-pty ./main.go

FROM ${NODE_IMAGE}

ARG GO_IMAGE
ARG NODE_IMAGE
ARG CODEX_VERSION
ARG CODEX_INTEGRITY
ARG SANDBOX_UID=10001
ARG SANDBOX_GID=10001

COPY --from=gateway-build /usr/local/libexec/validate-runner-build-args.sh /usr/local/libexec/validate-runner-build-args.sh
RUN sh /usr/local/libexec/validate-runner-build-args.sh "${GO_IMAGE}" "${NODE_IMAGE}" "${CODEX_VERSION}" "${CODEX_INTEGRITY}" \
  && apt-get update \
  && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    bash ca-certificates git openssh-client procps ripgrep tini \
  && mkdir -p /tmp/codex-package \
  && npm pack --ignore-scripts --json --pack-destination /tmp/codex-package "@openai/codex@${CODEX_VERSION}" >/tmp/codex-pack.json \
  && node -e 'const fs=require("fs");const value=JSON.parse(fs.readFileSync("/tmp/codex-pack.json","utf8"));if(value.length!==1||value[0].integrity!==process.argv[1]||!/^[A-Za-z0-9_.-]+\.tgz$/.test(value[0].filename)){process.exit(1)}' "${CODEX_INTEGRITY}" \
  && set -- /tmp/codex-package/*.tgz \
  && test "$#" -eq 1 \
  && npm install --global --omit=dev --ignore-scripts --no-audit --no-fund "$1" \
  && test "$(codex --version)" = "codex-cli ${CODEX_VERSION}" \
  && if ! getent group "${SANDBOX_GID}" >/dev/null; then groupadd --gid "${SANDBOX_GID}" sandbox; fi \
  && if ! getent passwd "${SANDBOX_UID}" >/dev/null; then \
       useradd --uid "${SANDBOX_UID}" --gid "${SANDBOX_GID}" --create-home --shell /bin/bash sandbox; \
     fi \
  && mkdir -p /workspace /codex \
  && chown "${SANDBOX_UID}:${SANDBOX_GID}" /workspace /codex \
  && npm cache clean --force \
  && rm -rf /var/lib/apt/lists/* /root/.npm /tmp/codex-package /tmp/codex-pack.json /usr/local/libexec/validate-runner-build-args.sh

COPY worksflow-sandbox-init /usr/local/bin/worksflow-sandbox-init
COPY --from=gateway-build /out/worksflow-sandbox-gateway /usr/local/bin/worksflow-sandbox-gateway
COPY --from=gateway-build /out/worksflow-sandbox-process /usr/local/bin/worksflow-sandbox-process
COPY --from=gateway-build /out/worksflow-sandbox-pty /usr/local/bin/worksflow-sandbox-pty
COPY runner-contract.json /usr/local/share/worksflow/runner-contract.json

RUN chmod 0555 /usr/local/bin/worksflow-sandbox-init \
  && chmod 0555 /usr/local/bin/worksflow-sandbox-gateway \
  && chmod 0555 /usr/local/bin/worksflow-sandbox-process \
  && chmod 0555 /usr/local/bin/worksflow-sandbox-pty \
  && chmod 0444 /usr/local/share/worksflow/runner-contract.json

ENV HOME=/tmp/home \
    CODEX_HOME=/codex \
    NO_UPDATE_NOTIFIER=1

WORKDIR /workspace
USER ${SANDBOX_UID}:${SANDBOX_GID}
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/worksflow-sandbox-init"]
