FROM alpine:latest

# Install SQLite runtime library (required by go-sqlite3)
RUN apk add --no-cache sqlite-libs ca-certificates

# Create app directory
WORKDIR /app

# Copy pre-built binary
COPY hn-reader /app/hn-reader

# Copy templates directory
COPY templates /app/templates

# Ensure binary is executable
RUN chmod +x /app/hn-reader

# Expose port
EXPOSE 8080

# Set environment variable for port (optional, defaults to 8080)
ENV PORT=8080

# Run the application
CMD ["/app/hn-reader"]
