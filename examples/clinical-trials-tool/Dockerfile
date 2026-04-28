FROM golang:1.25.7-alpine AS builder

RUN apk add --no-cache git make gcc musl-dev ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
COPY *.go ./

ENV GOWORK=off
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GONOSUMDB=github.com/truvaagents/truva-g3/*

RUN go mod download && go mod verify

RUN go build -a -installsuffix cgo \
    -ldflags '-w -s -extldflags "-static"' \
    -o clinical-trials-tool .

RUN ls -lh clinical-trials-tool

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /app/clinical-trials-tool .

RUN chown appuser:appgroup /app/clinical-trials-tool
USER appuser

ENV PORT=8367

EXPOSE ${PORT}

ENTRYPOINT ["./clinical-trials-tool"]
