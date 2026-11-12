# The PAR3 writer used only to create repair fixtures: one published rarpar
# release binary, fetched by URL and checked against its release digest. Never
# a source build and never a floating tag. No generated archive, parity file,
# or service image is published from this repository.
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS fetch

ARG RARPAR_URL
ARG RARPAR_SHA256

RUN set -eux; \
    apt-get update; \
    apt-get install --yes --no-install-recommends ca-certificates curl; \
    curl --fail --location --proto '=https' --tlsv1.2 --output /tmp/rarpar.tar.gz "$RARPAR_URL"; \
    echo "$RARPAR_SHA256  /tmp/rarpar.tar.gz" | sha256sum -c -; \
    mkdir -p /tmp/rarpar; \
    tar -xzf /tmp/rarpar.tar.gz -C /tmp/rarpar; \
    binary="$(find /tmp/rarpar -type f -name rarpar)"; \
    test "$(printf '%s\n' "$binary" | wc -l)" -eq 1; \
    install -D -m 0755 "$binary" /opt/rarpar/bin/rarpar

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

COPY --from=fetch /opt/rarpar/bin/rarpar /usr/local/bin/rarpar

ENTRYPOINT ["rarpar"]
