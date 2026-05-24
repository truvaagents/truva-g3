FROM golang:1.26.3-alpine AS builder
RUN apk add --no-cache git make gcc musl-dev ca-certificates
WORKDIR /app
COPY go.mod go.sum* ./
COPY *.go ./
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux GONOSUMDB=github.com/truvaagents/truva-g3/*
RUN go mod download && go mod verify
RUN go build -a -installsuffix cgo -ldflags '-w -s -extldflags "-static"' -o currency-global-tool .

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
RUN addgroup -g 1001 -S appgroup && adduser -u 1001 -S appuser -G appgroup
COPY --from=builder /app/currency-global-tool .
RUN chown appuser:appgroup /app/currency-global-tool && chmod +x /app/currency-global-tool
USER appuser
ENV PORT=8346
EXPOSE ${PORT}
ENTRYPOINT ["./currency-global-tool"]
