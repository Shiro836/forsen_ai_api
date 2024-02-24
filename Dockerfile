# golang app
FROM golang:1.22-alpine as builder

COPY . .

RUN go build -o app

ENTRYPOINT [ "app" ]
