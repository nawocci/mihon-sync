# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mihon-sync ./cmd/mihon-sync

FROM scratch
COPY --from=build /out/mihon-sync /mihon-sync
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENV MIHON_SYNC_ADDR=":8080" \
    MIHON_SYNC_DB="/data/mihon-sync.db"
VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["/mihon-sync"]
CMD ["serve"]
