# Build stage. The SQLite driver is pure Go, so no cgo is needed here or at runtime.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mihon-sync .

# Runtime stage.
FROM alpine:3.21
RUN adduser -D -H -u 1000 mihon
COPY --from=build /out/mihon-sync /usr/local/bin/mihon-sync
USER mihon
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/v1/health || exit 1
ENTRYPOINT ["mihon-sync"]
CMD ["serve", "--data", "/data", "--addr", ":8080"]
