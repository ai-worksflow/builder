# Both build arguments must be complete digest-pinned image references. The
# resulting Runner image is itself admitted and configured by its sha256 digest.
ARG GO_IMAGE
ARG NODE_IMAGE
ARG CODEX_VERSION
ARG CODEX_INTEGRITY

FROM ${GO_IMAGE} AS launcher-build
ARG GO_IMAGE
ARG NODE_IMAGE
ARG CODEX_VERSION
ARG CODEX_INTEGRITY
WORKDIR /src
COPY sandbox-runner/validate-runner-build-args.sh /usr/local/libexec/validate-runner-build-args.sh
RUN sh /usr/local/libexec/validate-runner-build-args.sh "${GO_IMAGE}" "${NODE_IMAGE}" "${CODEX_VERSION}" "${CODEX_INTEGRITY}"
COPY agent-runner/go.mod ./
COPY agent-runner/main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/worksflow-agent-runner ./main.go
COPY backend/cmd/agent-model-relay/main.go ./model-relay/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/worksflow-agent-model-relay ./model-relay

FROM ${NODE_IMAGE}
ARG GO_IMAGE
ARG NODE_IMAGE
ARG CODEX_VERSION
ARG CODEX_INTEGRITY
COPY --from=launcher-build /usr/local/libexec/validate-runner-build-args.sh /usr/local/libexec/validate-runner-build-args.sh
RUN sh /usr/local/libexec/validate-runner-build-args.sh "${GO_IMAGE}" "${NODE_IMAGE}" "${CODEX_VERSION}" "${CODEX_INTEGRITY}" \
    && mkdir -p /tmp/codex-package \
    && npm pack --ignore-scripts --json --pack-destination /tmp/codex-package "@openai/codex@${CODEX_VERSION}" >/tmp/codex-pack.json \
    && node -e 'const fs=require("fs");const value=JSON.parse(fs.readFileSync("/tmp/codex-pack.json","utf8"));if(value.length!==1||value[0].integrity!==process.argv[1]||!/^[A-Za-z0-9_.-]+\.tgz$/.test(value[0].filename)){process.exit(1)}' "${CODEX_INTEGRITY}" \
    && set -- /tmp/codex-package/*.tgz \
    && test "$#" -eq 1 \
    && npm install --global --omit=dev --ignore-scripts --no-audit --no-fund "$1" \
    && test "$(codex --version)" = "codex-cli ${CODEX_VERSION}" \
    && npm cache clean --force \
    && rm -rf /root/.npm /tmp/codex-package /tmp/codex-pack.json /usr/local/libexec/validate-runner-build-args.sh

COPY --from=launcher-build /out/worksflow-agent-runner /usr/local/bin/worksflow-agent-runner
COPY --from=launcher-build /out/worksflow-agent-model-relay /usr/local/bin/worksflow-agent-model-relay
COPY backend/internal/agent/schema/output.schema.json /opt/worksflow-agent/output.schema.json
COPY agent-runner/runner-contract.json /opt/worksflow-agent/runner-contract.json

RUN groupadd --gid 10001 worksflow \
    && useradd --uid 10001 --gid 10001 --no-create-home --home-dir /tmp/worksflow-home --shell /usr/sbin/nologin worksflow \
    && chmod 0555 /usr/local/bin/worksflow-agent-runner /usr/local/bin/worksflow-agent-model-relay \
    && chmod 0444 /opt/worksflow-agent/output.schema.json /opt/worksflow-agent/runner-contract.json

USER 10001:10001
ENV HOME=/tmp/worksflow-home \
    NO_COLOR=1
ENTRYPOINT ["/usr/local/bin/worksflow-agent-runner"]
