# Build stage — runs on the host's native arch and cross-compiles to the
# target platform (ACA requires linux/amd64), so no QEMU emulation on Apple
# Silicon. CGO off so the binary is fully static and runs on distroless.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src

# Module graph first so dependency downloads cache across code-only rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /overseer ./cmd/overseer

# Runtime stage — distroless/static ships CA certs (needed for Graph and
# Anthropic HTTPS calls) and nothing else; runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /overseer /overseer
EXPOSE 8080
ENTRYPOINT ["/overseer"]
