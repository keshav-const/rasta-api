# syntax=docker/dockerfile:1

# ---- build stage ----------------------------------------------------------
# Compile a fully static binary. pgx is pure Go, so CGO can be off — that lets
# us run on a tiny distroless image with no libc.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Download deps first so this layer is cached unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Then the source. -trimpath + -ldflags strip paths and debug info for a smaller,
# reproducible binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# ---- runtime stage --------------------------------------------------------
# distroless static: no shell, no package manager, runs as nonroot, and ships CA
# certs (needed for the TLS connection to Supabase). ~2 MB base.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/api /api

# The app binds :$PORT (default 8080). Render/most PaaS inject PORT at runtime.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]
