#!/usr/bin/env bash
# Host tuning for a PSG worker host (M9). Numbers are compared as decimal
# strings, so values beyond 64-bit integers (fs.file-max) still compare.
#
#   deploy/host-tune.sh            show current vs recommended settings (changes nothing)
#   sudo deploy/host-tune.sh --apply
#                                  write /etc/sysctl.d/90-psg.conf and load it
#
# The settings are for many SMPP connections at tens of thousands of PDUs per
# second: bigger accept and packet backlogs, more ephemeral ports and file
# handles, larger socket buffers, and low swappiness so a host short of memory
# fails fast (OOM-kills a process) instead of swapping until it stops
# answering. The memory check at the end warns when the host is shared or
# already swapping.
set -euo pipefail

CONF=/etc/sysctl.d/90-psg.conf
SETTINGS=(
  "net.core.somaxconn=65535"
  "net.ipv4.tcp_max_syn_backlog=65535"
  "net.core.netdev_max_backlog=250000"
  "net.ipv4.ip_local_port_range=1024 65535"
  "net.ipv4.tcp_fin_timeout=15"
  "net.ipv4.tcp_keepalive_time=300"
  "net.core.rmem_max=16777216"
  "net.core.wmem_max=16777216"
  "net.ipv4.tcp_rmem=4096 87380 16777216"
  "net.ipv4.tcp_wmem=4096 65536 16777216"
  "fs.file-max=2097152"
  "fs.nr_open=2097152"
  "vm.swappiness=10"
)

apply=0
case "${1:-}" in
  --apply) apply=1 ;;
  "") ;;
  *) echo "usage: $0 [--apply]" >&2; exit 2 ;;
esac

# Single numbers are minimums, except these, which are maximums; values with
# several numbers must match exactly. A setting already better is left alone.
LOWER_IS_BETTER=" vm.swappiness net.ipv4.tcp_fin_timeout net.ipv4.tcp_keepalive_time "
norm() { echo "$*" | tr -s ' \t' ' '; }
needs_change() { # KEY HAVE WANT
  local have want
  have=$(norm "$2") want=$(norm "$3")
  if [[ "$have" =~ ^[0-9]+$ && "$want" =~ ^[0-9]+$ ]]; then
    if [[ "$LOWER_IS_BETTER" == *" $1 "* ]]; then
      [ "$have" -gt "$want" ]
    else
      [ "${#have}" -lt "${#want}" ] || { [ "${#have}" -eq "${#want}" ] && [[ "$have" < "$want" ]]; }
    fi
  else
    [ "$have" != "$want" ]
  fi
}
printf '%-34s %-24s %-24s\n' "setting" "current" "recommended"
CHANGES=()
for s in "${SETTINGS[@]}"; do
  key=${s%%=*} want=${s#*=}
  have=$(sysctl -n "$key" 2>/dev/null || echo "n/a")
  mark=""
  if needs_change "$key" "$have" "$want"; then
    mark="  <-"
    CHANGES+=("$s")
  fi
  printf '%-34s %-24s %-24s%s\n' "$key" "$(norm "$have")" "$want" "$mark"
done

echo
echo "File handles: the worker container sets nofile=1048576 itself (docker-compose.yml)."
mem_total=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
mem_avail=$(awk '/MemAvailable/ {print int($2/1024)}' /proc/meminfo)
swap_total=$(awk '/SwapTotal/ {print int($2/1024)}' /proc/meminfo)
swap_free=$(awk '/SwapFree/ {print int($2/1024)}' /proc/meminfo)
echo "Memory: ${mem_avail} of ${mem_total} MB available; swap ${swap_free} of ${swap_total} MB free."
if [ "$swap_total" -gt 0 ] && [ $((swap_total - swap_free)) -gt $((swap_total / 2)) ]; then
  echo "WARNING: over half the swap is in use. Under load this host may thrash and stop"
  echo "         answering. Free memory, cap container memory, or move PSG to its own host."
fi
others=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -cv '^psg' || true)
if [ "${others:-0}" -gt 0 ]; then
  echo "NOTE: ${others} non-PSG containers run here. For load tests, PSG and the platform under"
  echo "      test should not share a host; if they must, give every container a memory limit."
fi

if [ "$apply" = 0 ]; then
  echo
  [ "${#CHANGES[@]}" -gt 0 ] && echo "${#CHANGES[@]} setting(s) to change. Run with --apply (as root) to write $CONF."
  exit 0
fi
[ "$(id -u)" = 0 ] || { echo "--apply needs root" >&2; exit 1; }
{
  echo "# Written by PSG deploy/host-tune.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  for s in "${CHANGES[@]}"; do echo "${s%%=*} = ${s#*=}"; done
} >"$CONF"
sysctl --load="$CONF" >/dev/null
echo "Wrote and loaded $CONF."
