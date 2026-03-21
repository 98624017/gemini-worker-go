FROM golang:1.25.8-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags="-s -w" -o /out/banana-async-gateway ./cmd/banana-async-gateway
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags="-s -w" -o /out/banana-async-migrate ./cmd/banana-async-migrate

FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
	&& adduser -D -H -u 10001 banana

WORKDIR /app

COPY --from=build /out/banana-async-gateway /usr/local/bin/banana-async-gateway
COPY --from=build /out/banana-async-migrate /usr/local/bin/banana-async-migrate
COPY --from=build /src/migrations /app/migrations

USER banana

EXPOSE 8080

ENTRYPOINT ["banana-async-gateway"]
