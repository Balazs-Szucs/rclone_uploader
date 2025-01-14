FROM golang:1.23-alpine AS build
RUN apk add --no-cache alpine-sdk

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

COPY .env /app/.env

RUN CGO_ENABLED=1 GOOS=linux go build -o main cmd/api/main.go

FROM alpine:3.20.1 AS prod
RUN apk add --no-cache tzdata
RUN apk add --no-cache --repository=http://dl-cdn.alpinelinux.org/alpine/edge/testing rclone

WORKDIR /app
COPY --from=build /app/main /app/main
COPY --from=build /app/.env /app/.env
COPY --from=build /app/internal/db/database.db /app/db/database.db
COPY --from=build /app/internal/static /app/internal/static

EXPOSE ${PORT}
CMD ["./main"]