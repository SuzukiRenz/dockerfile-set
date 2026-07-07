FROM alpine:edge as builder
LABEL stage=go-builder
WORKDIR /app/

RUN apk add --no-cache bash curl jq gcc git go musl-dev
RUN git clone https://github.com/OpenListTeam/OpenList.git ./ && ls -la
RUN go mod download
RUN bash build.sh release docker

FROM alpine:edge
USER root

ARG INSTALL_FFMPEG=false
ARG INSTALL_ARIA2=true
ARG UID=1001
ARG GID=1001

WORKDIR /opt/openlist/

RUN apk update && \
    apk upgrade --no-cache && \
    apk add --no-cache bash ca-certificates su-exec tzdata supervisor curl jq && \
    [ "$INSTALL_ARIA2" = "true" ] && apk add --no-cache aria2; \
    [ "$INSTALL_FFMPEG" = "true" ] && apk add --no-cache ffmpeg; \
    rm -rf /var/cache/apk/* && \
    mkdir -p /opt/openlist/data/aria2

COPY --from=builder /app/bin/openlist ./openlist
COPY entrypoint.sh /entrypoint.sh

RUN chmod +x /entrypoint.sh && \
    chmod +x ./openlist && \
    mkdir -p /opt/openlist/data && \
    chown -R ${UID}:${GID} /opt/openlist

ENV PUID=0 \
    PGID=0 \
    UMASK=022 \
    RUN_ARIA2=true \
    S3_ENABLE=true \
    S3_PORT=5246

VOLUME /opt/openlist/data/
EXPOSE 5244 5246

CMD [ "/entrypoint.sh" ]
