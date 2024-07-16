# Build stage
FROM golang:1.22.3-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target="/root/.cache/go-build" CGO_ENABLED=0 GOOS=linux go build -o network-passt-binding-sidecar ./cmd/sidecar

# Final stage
FROM registry.access.redhat.com/ubi9/ubi-minimal:9.3
WORKDIR /
COPY --from=build /app/network-passt-binding-sidecar /
CMD ["./network-passt-binding-sidecar"]
