FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o emby-proxy .

FROM alpine:3.21

RUN apk --no-cache add ca-certificates
COPY --from=builder /app/emby-proxy /usr/local/bin/emby-proxy

EXPOSE 8080

ENTRYPOINT ["emby-proxy"]
