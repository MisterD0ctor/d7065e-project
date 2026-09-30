#!/bin/sh
# Builds the password file from MQTT_PASSWORDS ("user:pass user:pass ...")
# at every start, so no hashed secrets live in the repository.
set -eu
umask 077
f=/mosquitto/data/passwd
: > "$f"
for pair in $MQTT_PASSWORDS; do
  mosquitto_passwd -b "$f" "${pair%%:*}" "${pair#*:}"
done
chown mosquitto:mosquitto "$f"
chmod 0700 "$f"
exec mosquitto -c /mosquitto/config/mosquitto.conf
