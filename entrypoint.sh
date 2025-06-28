#!/bin/bash
set -e  # Exit on error
# Auto-init Geth with genesis file if not yet initialized
if [ "$DISABLE_GENESIS_INIT" == "true" ]; then
  echo "⏭️ Skipping genesis initialization (DISABLE_GENESIS_INIT=true)"
  exec geth "$@"
fi

if [ -n "$GENESIS_FILE" ] && [ -n "$DATADIR" ]; then
  if [ ! -f "$DATADIR/geth/chaindata/CURRENT" ]; then
    echo "🟡 Initializing geth with genesis file: $GENESIS_FILE"
    geth --datadir "$DATADIR" init "$GENESIS_FILE"
  else
    echo "✅ Genesis already initialized in $DATADIR"
  fi
else
  echo "⚠️ GENESIS_FILE or DATADIR not provided — skipping init"
fi

# Launch geth with user-provided arguments
exec geth "$@"
