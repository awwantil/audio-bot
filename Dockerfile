FROM golang:1.24.2-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# First copy only dependency files
COPY go.mod go.sum ./
COPY upload upload

RUN go mod download

# Copy all source files including upload directory
COPY . .

# Build main package
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -v -a -installsuffix cgo \
    -ldflags '-extldflags "-static"' \
    -o /app/main ./cmd/main.go

FROM alpine:latest

RUN apk add --no-cache \
    ca-certificates \
    ffmpeg \
    python3 \
    py3-pip

RUN pip3 install --no-cache-dir --break-system-packages -U yt-dlp

WORKDIR /app

# Copy binaries and cookies
COPY --from=builder /app/main /app/main
#COPY --from=builder /app/upload/cookies.txt /app/upload/cookies.txt
COPY --from=builder /app/upload /app/upload

ENTRYPOINT ["/app/main"]