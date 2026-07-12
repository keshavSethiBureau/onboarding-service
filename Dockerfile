FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

# Create appuser (runs the final image as non-root).
ENV USER=appuser
ENV UID=10001

RUN adduser \
    --disabled-password \
    --gecos "" \
    --home "/nonexistent" \
    --shell "/sbin/nologin" \
    --no-create-home \
    --uid "${UID}" \
    "${USER}"

RUN apk add -U --no-cache ca-certificates git

# Go build cache + private-module access (Bureau modules are private).
ENV GOCACHE=/go/cache
RUN mkdir -p /go/cache
RUN go env -w GOPRIVATE=github.com/Bureau-Inc/*

ARG githubAccessToken
RUN git config --global url."https://golang:$githubAccessToken@github.com".insteadOf "https://github.com"

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build for the target platform selected by buildx (multi-arch aware).
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -ldflags="-w -s" -o onboarding-service ./cmd/server

##################################################################
FROM scratch

WORKDIR /app

# Import the user/group + CA certs from the builder.
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Binary + config files (runtime loads configs/<APP_ENV>.yml with ${VAR} expansion).
COPY --from=builder /app/onboarding-service /app/onboarding-service
COPY --from=builder /app/configs /app/configs

USER appuser:appuser

EXPOSE 8080

ENTRYPOINT ["/app/onboarding-service"]
