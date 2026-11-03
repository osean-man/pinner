# Build from the repository root:  docker build -f deploy/Dockerfile -t pinner-server .
# Base images are pinned by digest; Dependabot proposes updates.
FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
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
