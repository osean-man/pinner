# Build from the repository root:  docker build -f deploy/Dockerfile -t pinner-server .
# Base images are pinned by digest; Dependabot proposes updates.
FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
ARG VERSION=""
ARG COMMIT=""
ARG DATE=""
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/pinner-server ./cmd/pinner-server \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/pinner-server /pinner-server
# Owned by the nonroot user so a fresh named volume mounted here is writable.
COPY --from=build --chown=65532:65532 /out/data /data
ENV PINNER_ADDR=:8080 \
    PINNER_DB=/data/pinner-server.db
EXPOSE 8080
VOLUME /data
USER nonroot:nonroot
ENTRYPOINT ["/pinner-server"]
