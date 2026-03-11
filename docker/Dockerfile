# stage 1: build
FROM golang:1.23-alpine AS builder

WORKDIR /src

# download deps first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# copy source and build static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/scheduler ./cmd/scheduler

# stage 2: minimal runtime image
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata &&     adduser -D -u 1000 scheduler

COPY --from=builder /app/scheduler /app/scheduler

USER scheduler
EXPOSE 3000
ENTRYPOINT ["/app/scheduler"]
