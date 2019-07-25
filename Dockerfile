FROM golang:1.12 as builder

WORKDIR /app

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .
RUN go build -ldflags "-linkmode external -extldflags -static" -a main.go

FROM gcr.io/distroless/base
USER 65534:65534

COPY --from=builder /app/main /main
ENTRYPOINT ["/main"]
