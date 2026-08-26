# syntax=docker/dockerfile:1

# ---- build ------------------------------------------------------------
FROM golang:1.26 AS build

WORKDIR /src

# Dependencies are copied on their own so this layer is only rebuilt when
# go.mod or go.sum change, not on every source edit.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary, which is what lets the runtime
# stage be a distroless image with no libc at all.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime ----------------------------------------------------------
# distroless/static carries CA certificates (needed for SMTP over TLS) and
# nothing else: no shell, no package manager, nothing for an attacker who
# gets code execution to pivot with.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/server /server

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
