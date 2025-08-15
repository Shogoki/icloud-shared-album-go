FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy the entire project (including parent directory for the local module)
COPY . .

# Change to the API directory
WORKDIR /app/api

# Download dependencies (now the replace directive can find the parent module)
RUN go mod download

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o icloud-api-go main.go

# Use a minimal base image for the final image
FROM alpine:latest

WORKDIR /app

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Copy the binary from the builder stage
COPY --from=builder /app/api/icloud-api-go .

# Set default port
ENV PORT=80

# Expose port
EXPOSE 80

# Run the binary
CMD ["./icloud-api-go"]
