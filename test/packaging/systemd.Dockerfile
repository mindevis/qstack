# Debian 13 with systemd as PID 1, for installing and starting .deb packages
# in CI (test/packaging/deb-smoke.sh).
FROM debian:trixie
RUN apt-get update \
 && apt-get install -y --no-install-recommends systemd systemd-sysv curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
