# Build Stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod ./
# COPY go.sum ./ # No dependencies yet, so go.sum might not exist or be needed immediately

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Copy the source from the current directory to the Working Directory inside the container
COPY . .

# Build the Go app
# CGO_ENABLED=0 ensures a static binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o gemini-worker-go .

# Run Stage
FROM alpine:latest  

WORKDIR /root/

# Install CA certificates for HTTPS requests (essential for Gemini/Image fetching)
RUN apk --no-cache add ca-certificates

# Copy the Pre-built binary from the previous stage
COPY --from=builder /app/gemini-worker-go .

# Expose port (Documentation only, real port is dynamic)
EXPOSE 8787

# Command to run the executable
CMD ["./gemini-worker-go"]
