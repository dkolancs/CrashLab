FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY . .
ARG CMD=storefront
RUN CGO_ENABLED=0 go build -o /service ./cmd/${CMD}

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /service /service
EXPOSE 8080
ENTRYPOINT ["/service"]
