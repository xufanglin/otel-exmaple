FROM golang:alpine AS build-env

RUN apk update && apk add ca-certificates

WORKDIR /usr/src/app

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o otel-example -a -ldflags '-extldflags "-static"'

FROM scratch
COPY --from=build-env /usr/src/app/otel-example /otel-example
COPY --from=build-env /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

CMD ["/otel-example"]
