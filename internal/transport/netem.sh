#!/bin/sh
# Isolated, local-only transport compatibility test. Never modify the host qdisc.
set -eu
umask 077
if [ "${1:-}" != --inside ]; then
    test "$#" -eq 2
    exec unshare --user --map-current-user --keep-caps --net sh "$0" --inside "$1" "$2"
fi
# Even an accidental direct --inside invocation must not impair the host.
if [ "$(readlink /proc/self/ns/net)" = "$(readlink /proc/1/ns/net)" ]; then
    echo 'Refusing impairment in the initial network namespace' >&2
    exit 1
fi
artifact=$2
test_binary=$3
mkdir -p "$artifact"
ip link set lo up
tc qdisc add dev lo root netem delay 10ms loss 1% rate 2mbit seed 42
tcpdump --immediate-mode -U -i lo -s 0 -w "$artifact/local-netem.pcap" > "$artifact/capture.log" 2>&1 &
capture_pid=$!
trap 'kill -INT "$capture_pid" 2>/dev/null || true; wait "$capture_pid" 2>/dev/null || true' EXIT HUP INT TERM
"$test_binary" -test.v -test.timeout=45s -test.run='TestDirectTLSStagesAndMandatoryPin|TestExplicitConnectProxyAndRejection|TestHTTPSConnectAndInspectionRejection|TestRetryRecoveryAndProxyTimeout'
tc -s qdisc show dev lo
