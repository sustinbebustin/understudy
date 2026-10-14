#!/usr/bin/env bash
# Runs Probe.java against the Java Event Ruler pinned in pom.xml: cases on
# stdin, answers on stdout, one JSON object per line. Uses a local JDK 21+
# with Maven when both are on PATH, otherwise the pinned Maven image in
# Docker. RULERDIFF_RUNTIME=docker or =jdk forces one. Maven downloads from
# Maven Central into ~/.m2 (JDK) or the understudy-rulerdiff-m2 volume
# (Docker); nothing here talks to AWS.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
image=maven:3.9-eclipse-temurin-21@sha256:99e61abcff91a9b1333463bd8451fb18495d6eba9250ac66a338b518f8278320

runtime=${RULERDIFF_RUNTIME:-}
if [ -z "$runtime" ]; then
    if command -v java >/dev/null && command -v mvn >/dev/null; then
        runtime=jdk
    elif command -v docker >/dev/null; then
        runtime=docker
    else
        echo "rulerdiff: neither a JDK with Maven nor Docker is on PATH; install one to run the Java Ruler harness" >&2
        exit 2
    fi
fi

# The Maven step logs to stderr so stdout carries only the probe's answers.
probe='set -eu
lib=$(mktemp -d)
trap '\''rm -rf -- "$lib"'\'' EXIT
mvn -B -q -f "$1/pom.xml" dependency:copy-dependencies -DoutputDirectory="$lib" >&2
java -cp "$lib/*" "$1/Probe.java"'

case $runtime in
jdk)
    exec bash -c "$probe" probe "$here"
    ;;
docker)
    exec docker run --rm -i \
        -v "$here":/probe:ro -v understudy-rulerdiff-m2:/root/.m2 \
        "$image" bash -c "$probe" probe /probe
    ;;
*)
    echo "rulerdiff: RULERDIFF_RUNTIME is \"$runtime\"; use docker or jdk" >&2
    exit 2
    ;;
esac
