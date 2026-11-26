# Development machines without WPE packages only: Debian's WPE WebKit
# (the same legacy API as the device's) to build and run phone-webview.
FROM docker.io/library/debian:trixie
RUN apt-get update && apt-get install -y --no-install-recommends \
        gcc libc6-dev pkg-config libwpewebkit-2.0-dev libwpebackend-fdo-1.0-dev \
        libwpe-1.0-dev libwayland-dev libglib2.0-dev fonts-noto-cjk ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y --no-install-recommends make && rm -rf /var/lib/apt/lists/*
