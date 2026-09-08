# The GNU/Info-ZIP writer image: the upstream tools that write the tar, gzip,
# xz, zip and SFV lanes of the breadth corpus.
#
# Unlike RAR and 7-Zip there is no vendor tarball to pin by SHA-256 here: GNU
# tar, gzip, xz-utils, Info-ZIP zip/unzip and cksfv are distributed as
# distribution packages. The pin is therefore the same digest-pinned Debian
# base every other writer image is built on, plus the exact package versions
# that base resolves to, recorded inside the image and checked by the
# generator against docker/gnutools/toolchain.json before a single fixture
# byte is written. A Debian point release that moves any of them fails the
# build closed instead of quietly writing a fixture with a different writer.
#
# Info-ZIP `zip` is the upstream Info-ZIP codebase, not a re-implementation:
# it is the writer whose output the overwhelming majority of .zip posts carry,
# and its own `unzip` is what verifies the result here.
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

RUN set -eux; \
    apt-get update; \
    apt-get install --yes --no-install-recommends \
        tar gzip xz-utils bzip2 zip unzip cksfv coreutils; \
    dpkg-query --show --showformat='${Package} ${Version}\n' \
        tar gzip xz-utils bzip2 zip unzip cksfv > /usr/local/share/gnutools-versions.txt; \
    rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /usr/local/bin/gnutools

# One image, several tools: the first argument selects which, and everything
# after it is that tool's own argument vector, passed through untouched.
ENTRYPOINT ["/usr/local/bin/gnutools"]
