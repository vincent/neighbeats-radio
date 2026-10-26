FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /neighbeats-radio ./cmd/neighbeats-radio

# Piper (text to speech, CPU) and one English voice. Both are pinned; swap the voice with
# voice.model and a file in ./models.
FROM debian:bookworm-slim AS piper
ARG TARGETARCH
ARG PIPER_VERSION=2023.11.14-2
ARG VOICE=en_US-lessac-medium
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN case "$TARGETARCH" in arm64) arch=aarch64 ;; *) arch=x86_64 ;; esac \
 && curl -fsSL "https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/piper_linux_${arch}.tar.gz" | tar -xz -C /opt
RUN base=https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium \
 && curl -fsSL -o /opt/piper/voice.onnx "$base/${VOICE}.onnx" \
 && curl -fsSL -o /opt/piper/voice.onnx.json "$base/${VOICE}.onnx.json"

FROM debian:bookworm-slim
COPY --from=piper /opt/piper /opt/piper
COPY --from=build /neighbeats-radio /neighbeats-radio
ENTRYPOINT ["/neighbeats-radio"]
CMD ["run"]
