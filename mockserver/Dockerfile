# syntax=docker/dockerfile:1

# --- build ---
FROM golang:1.23-alpine AS build
WORKDIR /src
# Modules first for layer caching (no external deps, but keeps the pattern).
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mockserver .

# --- run ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mockserver /mockserver
EXPOSE 8080
ENV PORT=8080
USER nonroot:nonroot
ENTRYPOINT ["/mockserver"]
