FROM dhi.io/golang:1.27-alpine3.24-dev@sha256:72cac473a0b802b521e23a2aed591ad166645729533d6d3db4ce96ae22c697ff AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -o /out/whatsapp-mcp ./cmd/whatsapp-mcp

FROM dhi.io/alpine-base:3.23-dev@sha256:c8b128dc993e8cdaf2972ea5b005b74363c634ba499ae6a99a41f22504899472 AS ffmpeg-build
RUN mkdir -p /out/ffmpeg /out/data /out/bin \
    && touch /out/data/.volume \
    && chmod 0700 /out/data \
    && printf '%s\n' https://dl-cdn.alpinelinux.org/alpine/v3.23/main https://dl-cdn.alpinelinux.org/alpine/v3.23/community > /tmp/alpine-repositories \
    && apk --root /out/ffmpeg --initdb --no-scripts --no-cache --repositories-file /tmp/alpine-repositories --keys-dir /etc/apk/keys add ffmpeg \
    && printf '%s\n' '#!/bin/sh' 'set -e' 'root=/opt/ffmpeg' 'for loader in "$root"/lib/ld-musl-*.so.1; do' '    exec "$loader" --library-path "$root/lib:$root/usr/lib:$root/usr/lib/pulseaudio" "$root/usr/bin/ffmpeg" "$@"' 'done' > /out/bin/ffmpeg \
    && chmod 0755 /out/bin/ffmpeg

FROM dhi.io/static:20260909-alpine3.24@sha256:069a5570611dffc0100e35cd0954ced49fb1299c99f25797d778b9ea73868b9c AS gateway
COPY --from=build /out/whatsapp-mcp /usr/local/bin/whatsapp-mcp
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/whatsapp-mcp"]

FROM dhi.io/alpine-base:3.23@sha256:8a14cb527704fd6325fae6e92e0bc73a3c64b44a83c36a3abb53730c5757cc72 AS worker
COPY --from=build /out/whatsapp-mcp /usr/local/bin/whatsapp-mcp
COPY --from=ffmpeg-build /out/ffmpeg/ /opt/ffmpeg/
COPY --from=ffmpeg-build /out/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg-build --chown=65532:65532 /out/data/ /data/
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/whatsapp-mcp"]
CMD ["--worker"]
