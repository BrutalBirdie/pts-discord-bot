# renovate: datasource=docker depName=golang
ARG GO_IMAGE=golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9

FROM ${GO_IMAGE} AS build

# Must match the libdave version godave was built against: https://github.com/disgoorg/godave/blob/master/libdave/release.txt
ARG LIBDAVE_VERSION=v1.1.0
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends unzip pkg-config && rm -rf /var/lib/apt/lists/*

RUN case "${TARGETARCH:-amd64}" in amd64) arch=X64 ;; arm64) arch=ARM64 ;; *) echo "unsupported arch ${TARGETARCH}" && exit 1 ;; esac \
    && curl -fsSL -o /tmp/libdave.zip "https://github.com/discord/libdave/releases/download/${LIBDAVE_VERSION}/cpp/libdave-Linux-${arch}-boringssl.zip" \
    && unzip -j -o /tmp/libdave.zip include/dave/dave.h -d /usr/local/include \
    && unzip -j -o /tmp/libdave.zip lib/libdave.so -d /usr/local/lib \
    && rm /tmp/libdave.zip \
    && mkdir -p /usr/local/lib/pkgconfig \
    && printf 'Name: dave\nDescription: libdave\nVersion: %s\nLibs: -L/usr/local/lib -ldave\nCflags: -I/usr/local/include\n' "${LIBDAVE_VERSION}" > /usr/local/lib/pkgconfig/dave.pc \
    && ldconfig

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
RUN go test ./... && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/voicebot .

FROM cloudron/base:6.0.1@sha256:7b1e729960c3ca312d26e886917ba89483241bba0219ff292858e10d9d5768ee

# renovate: datasource=deb depName=ffmpeg
ARG FFMPEG_VERSION=7:8.0.1-3ubuntu2

LABEL org.opencontainers.image.source=https://github.com/BrutalBirdie/pts-discord-bot

RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg=${FFMPEG_VERSION} && rm -rf /var/cache/apt /var/lib/apt/lists

RUN mkdir -p /app/code /app/pkg
WORKDIR /app/code

COPY --from=build /usr/local/lib/libdave.so /usr/local/lib/
RUN ldconfig
COPY --from=build /out/voicebot /app/code/voicebot
COPY env.sh.template start.sh /app/pkg/

CMD [ "/app/pkg/start.sh" ]
