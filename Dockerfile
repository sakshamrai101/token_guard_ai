FROM golang:1.24-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /proxy ./cmd/proxy

FROM alpine:3.21

RUN apk add --no-cache ca-certificates \
	&& adduser -D -H -u 65532 proxy

COPY --from=builder /proxy /proxy

USER proxy
EXPOSE 8080
ENTRYPOINT ["/proxy"]
