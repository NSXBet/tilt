# Single-stage on purpose: live_update rebuilds the binary inside the
# running container, which needs the Go toolchain there.
FROM golang:1.22-alpine
WORKDIR /app
COPY go.mod ./
COPY . .
RUN go build -o server .
CMD ["/app/server"]
