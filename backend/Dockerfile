FROM golang:1.22.6-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.20

RUN apk add --no-cache ca-certificates docker-cli wget \
  && addgroup -S -g 10001 worksflow \
  && adduser -S -D -H -u 10001 -G worksflow worksflow \
  && mkdir -p /var/lib/worksflow/quality /var/lib/worksflow/published \
  && chown -R worksflow:worksflow /var/lib/worksflow
COPY --from=build /out/api /usr/local/bin/api

USER worksflow
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
