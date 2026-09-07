# UI stage. Node is needed to build the admin interface and nothing else — it
# does not reach the final image.
FROM node:22-alpine AS ui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Build stage. CGO_ENABLED=0 is load-bearing: modernc.org/sqlite is pure Go, so
# the result is a static binary that runs in a scratch image with no runtime.
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Staged where go:embed picks it up, so the binary carries its own interface.
COPY --from=ui /web/dist/ ./internal/httpapi/webdist/

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X github.com/nebuloss/claudication/internal/version.Version=${VERSION} \
        -X github.com/nebuloss/claudication/internal/version.Commit=${COMMIT} \
        -X github.com/nebuloss/claudication/internal/version.Date=${DATE}" \
      -o /claudication ./cmd/claudication

# Runtime: distroless, non-root. Nothing but the binary ships — no package
# manager, no shell, no build tooling.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /claudication /usr/local/bin/claudication

# The state directory must be a mounted volume. claudication refuses to start
# if it is not writable, which turns the "credentials vanish on container
# recreate" misconfiguration into a startup error instead of silent data loss.
ENV CLAUDICATION_STATE_DIR=/var/lib/claudication
VOLUME ["/var/lib/claudication"]
EXPOSE 8317
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/claudication"]
CMD ["serve"]
