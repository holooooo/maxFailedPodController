FROM golang:1.13-alpine AS builder

ENV GO111MODULE=on \
  CGO_ENABLED=0 \
  GOOS=linux \
  GOARCH=amd64

WORKDIR /src
COPY . .

RUN go build -mod vendor -o releases/controller

FROM alpine:latest

RUN addgroup -g 1001 appgroup && \
  adduser -H -D -s /bin/false -G appgroup -u 1001 appuser

USER 1001:1001
COPY --from=builder /src/releases/controller /bin/controller
ENTRYPOINT ["/bin/controller"]