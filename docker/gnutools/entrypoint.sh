#!/bin/sh
# Selects one of the pinned writer image's tools. The remaining arguments are
# exec'd as that tool's own argument vector: nothing here is re-parsed,
# expanded or passed through a shell.
set -eu

if [ "$#" -lt 1 ]; then
    echo "usage: <tar|gzip|xz|bzip2|zip|unzip|cksfv|versions> [arguments...]" >&2
    exit 2
fi

tool="$1"
shift

case "$tool" in
    versions)
        exec cat /usr/local/share/gnutools-versions.txt
        ;;
    tar|gzip|xz|bzip2|zip|unzip|cksfv)
        exec "$tool" "$@"
        ;;
    *)
        echo "unknown tool: $tool (expected tar, gzip, xz, bzip2, zip, unzip, cksfv or versions)" >&2
        exit 2
        ;;
esac
