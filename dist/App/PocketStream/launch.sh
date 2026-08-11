#!/bin/sh

appdir=/mnt/SDCARD/App/PocketStream
sysdir=/mnt/SDCARD/.tmp_update
networklog="$appdir/network.log"
tpwslog="$appdir/tpws.log"
pocketlog="$appdir/pocketstream.log"
governor_path=/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor
previous_governor=
tpws_pid=

# Logs and history live on a removable FAT card, where Unix mode bits are not a
# confidentiality boundary.  Still create new files conservatively, cap their
# size, and discard pre-release diagnostics that contained network identifiers.
umask 077
privacy_marker="$appdir/.privacy-log-format-v1"
if [ ! -f "$privacy_marker" ]; then
    rm -f "$networklog" "$networklog.1" "$tpwslog" "$tpwslog.1" \
        "$pocketlog" "$pocketlog.1" "$appdir/zapret-check.log" \
        "$appdir/zapret-check.sh" "$appdir/zapret/nfqws"
    : > "$privacy_marker"
fi

rotate_log() {
    log_file=$1
    max_bytes=262144
    [ -f "$log_file" ] || return 0
    log_size=$(wc -c < "$log_file" 2>/dev/null)
    case "$log_size" in
        ''|*[!0-9]*) return 0 ;;
    esac
    if [ "$log_size" -gt "$max_bytes" ]; then
        rm -f "$log_file.1"
        mv "$log_file" "$log_file.1"
    fi
}

rotate_log "$networklog"
rotate_log "$tpwslog"
rotate_log "$pocketlog"

cleanup() {
    status=$?
    trap - 0 1 2 15
    if [ -n "$tpws_pid" ]; then
        kill "$tpws_pid" 2>/dev/null
        wait "$tpws_pid" 2>/dev/null
    fi
    rm -f /tmp/stay_awake
    if [ -n "$previous_governor" ] && [ -w "$governor_path" ]; then
        echo "$previous_governor" > "$governor_path" 2>/dev/null
    fi
    exit "$status"
}

on_signal() {
    exit 130
}

trap cleanup 0
trap on_signal 1 2 15

export LD_LIBRARY_PATH="$sysdir/lib:$sysdir/lib/parasyte:${LD_LIBRARY_PATH}"
# OnionOS ships without a usable system CA store on some Miyoo images.
# Point Go's x509 verifier at PocketStream's current Mozilla CA bundle.
export SSL_CERT_FILE="$appdir/cacert.pem"
# Current Go releases enable larger post-quantum ClientHello messages. They are
# disabled only for compatibility with the Miyoo kernel and the local network
# helper. TLS certificate and hostname verification remain enabled.
export GODEBUG=tlsmlkem=0,tlssecpmlkem=0

if [ -r "$governor_path" ] && [ -w "$governor_path" ]; then
    previous_governor=$(cat "$governor_path" 2>/dev/null)
    echo performance > "$governor_path"
fi

touch /tmp/stay_awake

# Onion can report Wi-Fi as enabled before DHCP has installed a default route.
# Recover it using the same components as Onion's OTA updater, but never wait
# forever: PocketStream must still open and show an offline error if DHCP fails.
if ! awk 'NR > 1 && $2 == "00000000" { found=1 } END { exit !found }' /proc/net/route; then
    {
        echo "--- `date` DHCP recovery ---"

        # wlan0 does not exist until the Miyoo's USB Wi-Fi module is loaded.
        # Onion's OTA updater performs this step explicitly as well.
        if ! ifconfig wlan0 >/dev/null 2>&1; then
            insmod /mnt/SDCARD/8188fu.ko 2>/dev/null || true
            ifconfig lo up
        fi
        /customer/app/axp_test wifion >/dev/null 2>&1

        wifi_wait=0
        while ! ifconfig wlan0 >/dev/null 2>&1 && [ "$wifi_wait" -lt 6 ]; do
            sleep 1
            wifi_wait=$((wifi_wait + 1))
        done

        if ifconfig wlan0 >/dev/null 2>&1; then
            unset LD_PRELOAD
            ifconfig wlan0 up

            # A stale supplicant/udhcpc pair can survive a Wi-Fi power cycle.
            # Restart both only because the route check above already failed.
            if pgrep wpa_supplicant >/dev/null 2>&1; then
                killall wpa_supplicant
                sleep 1
            fi
            if pgrep udhcpc >/dev/null 2>&1; then
                killall udhcpc
                sleep 1
            fi
            if /mnt/SDCARD/miyoo/app/wpa_supplicant -B -D nl80211 -iwlan0 -c /appconfigs/wpa_supplicant.conf >/dev/null 2>&1; then
                echo "Wi-Fi supplicant started"
            else
                echo "Wi-Fi supplicant failed"
            fi
            sleep 2
            if udhcpc -i wlan0 -s /etc/init.d/udhcpc.script -n -q -t 6 -T 2 >/dev/null 2>&1; then
                echo "DHCP recovery completed"
            else
                echo "DHCP recovery failed"
            fi
        else
            echo "wlan0 did not appear after loading 8188fu.ko"
        fi

        if ifconfig wlan0 >/dev/null 2>&1; then
            echo "wlan0 available"
        else
            echo "wlan0 unavailable"
        fi
        if awk 'NR > 1 && $2 == "00000000" { found=1 } END { exit !found }' /proc/net/route; then
            echo "default route available"
        else
            echo "default route unavailable"
        fi
    } >> "$networklog" 2>&1
fi

# PocketStream must never change the console's global clock from an
# unauthenticated network response. OnionOS owns time synchronization. Record a
# coarse warning only; do not store timezone, IP, MAC, or full command output.
clock_year=$(date -u +%Y 2>/dev/null)
case "$clock_year" in
    ''|*[!0-9]*) echo "system clock unavailable; HTTPS may fail" >> "$networklog" ;;
    *)
        if [ "$clock_year" -lt 2024 ]; then
            echo "system clock is too old; set time in OnionOS before streaming" >> "$networklog"
        fi
        ;;
esac

# The Miyoo kernel has no NFQUEUE/iptables support, so use a userspace SOCKS
# compatibility layer and route only PocketStream through it.
chmod 700 "$appdir/zapret/tpws"
chmod 700 "$appdir/ffmpeg/ffmpeg"
"$appdir/zapret/tpws" \
    --socks --bind-addr=127.0.0.1 --port=987 --debug=0 \
    --filter-tcp=443 --hostlist-domains=googlevideo.com --split-pos=2 --disorder --new \
    --filter-tcp=80 --methodeol --new \
    --filter-tcp=443 --split-pos=1,midsld --disorder \
    >> "$tpwslog" 2>&1 &
tpws_pid=$!
sleep 1
if kill -0 "$tpws_pid" 2>/dev/null; then
    export POCKETSTREAM_SOCKS5=127.0.0.1:987
else
    echo "tpws failed to start" >> "$tpwslog"
fi

"$appdir/pocketstream" >> "$appdir/pocketstream.log" 2>&1
status=$?
exit $status
