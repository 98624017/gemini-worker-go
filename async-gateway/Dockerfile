FROM golang:1.25.8-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -o /out/banana-async-gateway ./cmd/banana-async-gateway

FROM alpine:3.20

RUN adduser -D -H -u 10001 banana

WORKDIR /app

COPY --from=build /out/banana-async-gateway /usr/local/bin/banana-async-gateway

USER banana

EXPOSE 8080

ENTRYPOINT ["banana-async-gateway"]
