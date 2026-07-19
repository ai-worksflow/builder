ARG GO_IMAGE
ARG RUNTIME_IMAGE

FROM ${GO_IMAGE} AS build
ARG GO_IMAGE
ARG RUNTIME_IMAGE

WORKDIR /src
RUN for image in "${GO_IMAGE}" "${RUNTIME_IMAGE}"; do \
      printf '%s\n' "${image}" | grep -Eq '^[^[:space:]@]+@sha256:[0-9a-f]{64}$' || exit 64; \
    done
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agent-model-relay ./cmd/agent-model-relay \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/template-authority ./cmd/template-authority \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/production-postgres-posture ./cmd/production-postgres-posture \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/repository-index-gc ./cmd/repository-index-gc

FROM ${RUNTIME_IMAGE}

RUN apk add --no-cache ca-certificates docker-cli git podman-remote wget \
  && ln -s /usr/bin/podman-remote /usr/local/bin/podman \
  && addgroup -S -g 10001 worksflow \
  && adduser -S -D -H -u 10001 -G worksflow worksflow \
  && mkdir -p /home/worksflow /var/lib/worksflow/quality /var/lib/worksflow/published /var/lib/worksflow/template-sources \
  && chown -R worksflow:worksflow /home/worksflow /var/lib/worksflow
COPY --from=build /out/api /usr/local/bin/api
COPY --from=build /out/agent-model-relay /usr/local/bin/agent-model-relay
COPY --from=build /out/template-authority /usr/local/bin/template-authority
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=build /out/production-postgres-posture /usr/local/bin/production-postgres-posture
COPY --from=build /out/repository-index-gc /usr/local/bin/repository-index-gc

USER worksflow
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
