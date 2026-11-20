FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod delayd.go ./
RUN CGO_ENABLED=0 go build -o /delayd .

FROM alpine:3.20
RUN apk add --no-cache iproute2 iptables
COPY entrypoint.sh /entrypoint.sh
COPY --from=build /delayd /usr/local/bin/delayd
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
