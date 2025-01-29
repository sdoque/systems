
# Documentation: https://docs.docker.com/reference/dockerfile/

FROM golang:1.23-alpine AS builder

WORKDIR /src/

# Copy and download dependencies first (utilising docker's build caches in a better way)
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and compile it
ARG SRC
COPY $SRC ./
RUN go build -o system

################################################################################

# Run a clean container runtime (without any build tools)
FROM alpine:3.21 AS final

# Install dependencies
RUN apk add curl

# Copies generated files from the builder
COPY --from=builder /src/system /
WORKDIR /data

# Runs periodic healthchecks on the system
ARG PORT
HEALTHCHECK --interval=1m CMD curl -f "http://localhost:$PORT/" || exit 1

# Run the system
CMD ["/system"]
