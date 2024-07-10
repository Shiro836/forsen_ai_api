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
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bin/app ./cmd/app/main.go

# Step 3: Final
FROM scratch
COPY --from=builder /app/cfg /cfg
COPY --from=builder /bin/app /app
EXPOSE 8080
CMD ["/app", "--cfg-path", "/cfg/docker_cfg.yaml"]
