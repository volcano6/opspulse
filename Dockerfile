# ---- Build ----
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/opspulse ./cmd/opspulse

# ---- Runtime ----
FROM alpine:3.21
RUN apk --no-cache add ca-certificates tzdata && \
    adduser -D opspulse
COPY --from=builder /bin/opspulse /usr/local/bin/opspulse
USER opspulse
ENTRYPOINT ["opspulse"]
