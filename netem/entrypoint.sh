#!/bin/sh
# Inject latency with tc netem when the kernel supports it.
# Otherwise hold a userspace delay proxy and an iptables redirect in this network namespace.
set -eu

dev=$(ip -o link show | awk -F': ' '$2 != "lo" { print $2; exit }')
dev=${dev%%@*}

cmd=${1:-}
case "$cmd" in
  inject)
    delay=${2:?delay required}
    if [ -z "${dev}" ]; then
      echo "no non-loopback interface" >&2
      exit 1
    fi
    tc qdisc replace dev "$dev" root netem delay "$delay" \
      || tc qdisc add dev "$dev" root netem delay "$delay"
    ;;
  inject-loss)
    percent=${2:?percent required}
    if [ -z "${dev}" ]; then
      echo "no non-loopback interface" >&2
      exit 1
    fi
    tc qdisc replace dev "$dev" root netem loss "${percent}%" \
      || tc qdisc add dev "$dev" root netem loss "${percent}%"
    ;;
  clear)
    if [ -n "${dev}" ]; then
      tc qdisc del dev "$dev" root >/dev/null 2>&1 || true
    fi
    if command -v iptables >/dev/null 2>&1; then
      iptables -t nat -S PREROUTING 2>/dev/null | while read -r line; do
        case "$line" in
          *crashlab-delay*)
            # shellcheck disable=SC2086
            set -- $line
            shift
            iptables -t nat -D "$@" || true
            ;;
        esac
      done
    fi
    ;;
  show)
    if [ -z "${dev}" ]; then
      echo "no non-loopback interface" >&2
      exit 1
    fi
    tc qdisc show dev "$dev"
    if command -v iptables >/dev/null 2>&1; then
      iptables -t nat -S PREROUTING || true
    fi
    ;;
  fallback)
    delay=${2:?delay required}
    port=${3:?port required}
    iptables -t nat -C PREROUTING -p tcp --dport "$port" -m comment --comment crashlab-delay -j REDIRECT --to-ports 18080 2>/dev/null \
      || iptables -t nat -A PREROUTING -p tcp --dport "$port" -m comment --comment crashlab-delay -j REDIRECT --to-ports 18080
    # Existing pooled connections would skip the new redirect. Drop them so clients reconnect.
    ss -K -t state established sport = :"$port" >/dev/null 2>&1 || true
    exec /usr/local/bin/delayd -listen "0.0.0.0:18080" -upstream "127.0.0.1:${port}" -delay "$delay"
    ;;
  fallback-loss)
    percent=${2:?percent required}
    port=${3:?port required}
    iptables -t nat -C PREROUTING -p tcp --dport "$port" -m comment --comment crashlab-delay -j REDIRECT --to-ports 18080 2>/dev/null \
      || iptables -t nat -A PREROUTING -p tcp --dport "$port" -m comment --comment crashlab-delay -j REDIRECT --to-ports 18080
    ss -K -t state established sport = :"$port" >/dev/null 2>&1 || true
    exec /usr/local/bin/delayd -listen "0.0.0.0:18080" -upstream "127.0.0.1:${port}" -delay 0s -loss "${percent}"
    ;;
  *)
    echo "usage: inject <delay> | inject-loss <percent> | clear | show | fallback <delay> <port> | fallback-loss <percent> <port>" >&2
    exit 2
    ;;
esac
