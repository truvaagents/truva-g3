FROM golang:1.27.0-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum* ./
COPY *.go ./
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux GONOSUMDB=github.com/truvaagents/truva-g3/*
RUN go mod download && go build -ldflags '-w -s -extldflags "-static"' -o demographics-tool .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
RUN adduser -u 1001 -S appuser
COPY --from=builder /app/demographics-tool .
USER appuser
ENV PORT=8365
EXPOSE ${PORT}
ENTRYPOINT ["./demographics-tool"]
