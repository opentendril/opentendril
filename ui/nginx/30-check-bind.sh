#!/bin/sh
if [ "$UI_BIND" = "0.0.0.0" ]; then
    echo "==============================================================================="
    echo "WARNING: UI_BIND is set to 0.0.0.0 (exposed to LAN/Internet)."
    echo "Ensure your host Go Stem has OPENTENDRIL_API_KEY configured."
    echo "Failing to do so will expose your Command Center and OS of OT to unauthorized access."
    echo "==============================================================================="
fi
