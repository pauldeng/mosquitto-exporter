# gcr.io/distroless/static-debian12:nonroot already ships an up-to-date CA
# bundle (/etc/ssl/certs/ca-certificates.crt) and a non-root "nonroot" user
# (uid/gid 65532) - no apt-get, no builder stage needed for either. Pinned by
# digest for reproducibility; bump both the tag and digest together.
FROM       gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL      source_repository="https://github.com/pauldeng/mosquitto-exporter"
COPY       mosquitto-exporter /mosquitto_exporter
EXPOSE     9234
USER       nonroot:nonroot
ENTRYPOINT ["/mosquitto_exporter"]
