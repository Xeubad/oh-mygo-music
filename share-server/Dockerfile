FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/share-server ./cmd/share-server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates ffmpeg \
    && addgroup -S -g 10001 share \
    && adduser -S -D -H -u 10001 -G share share \
    && mkdir -p /var/lib/share-server \
    && chown share:share /var/lib/share-server
COPY --from=build /out/share-server /usr/local/bin/share-server
USER share:share
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/share-server"]
