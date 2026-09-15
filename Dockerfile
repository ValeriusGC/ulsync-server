# Multi-stage static image. CGO is off so the runtime can be distroless
# (no shell, no curl). Do not add VOLUME: an anonymous volume hides data
# loss on container recreate. The named volume lives in compose.yaml.
# Runtime USER 65532: after copying ulsync.db into the volume from the host,
# chown 65532:65532 or push returns 503 while pull/SSE still work.

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN mkdir -p /out /data
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/ulsync-server ./cmd/ulsync-server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/ulsync-server /ulsync-server
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/ulsync-server"]
CMD ["-config", "/etc/ulsync/config.yaml"]
HEALTHCHECK --interval=5s --timeout=3s --start-period=3s --retries=3 \
    CMD ["/ulsync-server", "-healthcheck"]
