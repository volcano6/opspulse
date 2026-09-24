# ---- Build ----
FROM golang:1.26-alpine AS builder
WORKDIR /src

# The official proxy is the default, but it can be overridden so the image also
# builds on networks that cannot reach it (e.g. behind a regional mirror).
# Build args are scoped to this stage: the runtime image below carries none.
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/volcano6/opspulse/internal/version.Version=${VERSION} -X github.com/volcano6/opspulse/internal/version.Commit=${COMMIT} -X github.com/volcano6/opspulse/internal/version.Date=${DATE}" \
    -o /bin/opspulse ./cmd/opspulse

# ---- Runtime ----
FROM alpine:3.21
RUN apk --no-cache add ca-certificates tzdata && \
    adduser -D opspulse
COPY --from=builder /bin/opspulse /usr/local/bin/opspulse
USER opspulse
ENTRYPOINT ["opspulse"]
