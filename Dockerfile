# Build stage
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /emberfalld ./cmd/emberfalld

# Runtime stage: single static binary
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /emberfalld /usr/local/bin/emberfalld
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/emberfalld"]
