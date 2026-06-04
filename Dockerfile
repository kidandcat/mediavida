# Build stage
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mediavida-server .

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && update-ca-certificates
ENV TZ=Europe/Madrid
WORKDIR /app
COPY --from=build /out/mediavida-server /app/mediavida-server
# Session/cookie state lives under $HOME/.config/mediavida-mcp
ENV HOME=/app
EXPOSE 8080
CMD ["/app/mediavida-server", "-addr", ":8080"]
