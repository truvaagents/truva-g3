# TEST DOUBLE for the OpenClaw gateway sidecar (the real image is not available).
# Builds a tiny static stub serving POST /v1/responses. Build + tag as openclaw:local:
#   docker build -t openclaw:local examples/openclaw-tool/mock-openclaw
FROM golang:1.27.0-alpine AS build
WORKDIR /app
COPY go.mod main.go ./
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -ldflags '-w -s' -o mock-openclaw .

FROM alpine:3.23
COPY --from=build /app/mock-openclaw /usr/local/bin/mock-openclaw
EXPOSE 18789
ENTRYPOINT ["/usr/local/bin/mock-openclaw"]
