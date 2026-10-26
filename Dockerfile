FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /neighbeats-radio ./cmd/neighbeats-radio

FROM scratch
COPY --from=build /neighbeats-radio /neighbeats-radio
ENTRYPOINT ["/neighbeats-radio"]
CMD ["run"]
