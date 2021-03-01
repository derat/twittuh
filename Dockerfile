# Stage 0: Compile twittuh.
FROM golang:1.16-buster
WORKDIR /go/src/twittuh
COPY . .
RUN go get -d -v ./...
RUN go install -v ./...

# Stage 1: Install Chrome and other dependencies and run tor and twittuh.
FROM google/cloud-sdk:slim

# The "apt" command prints annoying warnings about not having a stable CLI
# interface, but "apt-get" doesn't seem to support installing a local .deb file
# and its dependencies.
RUN apt update
RUN apt install -y procps tor wget
RUN echo "ControlPort 9051" >>/etc/tor/torrc
RUN echo "CookieAuthentication 0" >>/etc/tor/torrc
RUN wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
RUN apt install -y ./google-chrome-stable_current_amd64.deb

COPY --from=0 /go/bin/twittuh .
RUN mkdir -p /tmp/twittuh-cache
EXPOSE 8080/tcp
CMD \
  /etc/init.d/tor start ; \
  ./twittuh -verbose \
    -cache-dir /tmp/twittuh-cache \
    -fetch-timeout 90 \
    -tweet-timeout 35 \
    -page-settle-delay 10 \
    -proxy socks5://localhost:9050 \
    -tor-control localhost:9051 \
    -serve 0.0.0.0:8080
