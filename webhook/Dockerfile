FROM golang:1.21-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /webhook .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /webhook /webhook
EXPOSE 8443 8080
ENTRYPOINT ["/webhook"]
