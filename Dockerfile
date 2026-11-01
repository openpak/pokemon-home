FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags "-s -w" -o /out/pokemon-home .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 home && mkdir /capture && chown home /capture
USER home
ENV HOME_HTTP_ADDR=:20200
EXPOSE 20200
COPY --from=build /out/pokemon-home /usr/local/bin/pokemon-home
ENTRYPOINT ["pokemon-home"]
