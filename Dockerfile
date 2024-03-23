# Step 1: Modules caching
FROM golang:1.22-alpine as modules
COPY go.mod go.sum /modules/
WORKDIR /modules
RUN go mod download

# Step 2: Builder
FROM golang:1.22-alpine as builder
COPY --from=modules /go/pkg /go/pkg
COPY . /app
WORKDIR /app
RUN go build -o /bin/app ./cmd/app/main.go

# Step 3: Final
FROM scratch
COPY --from=builder /bin/app /app
CMD ["/app", "--cfg-path", "/app/cfg/docker_cfg.yaml"]
