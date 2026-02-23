package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
)

type WebhookSignatureData struct {
	ShipmentID string `json:"shipment_id"`
	OrderID    string `json:"order_id"`
	EventType  string `json:"event_type"`
	Timestamp  int64  `json:"timestamp"`
}

func computeWebhookHMAC(rawBody []byte, secret string) string {
	var sigData WebhookSignatureData
	if err := json.Unmarshal(rawBody, &sigData); err != nil {
		log.Printf("[webhook] Failed to unmarshal payload for HMAC: %v", err)
		return ""
	}

	canonical, err := json.Marshal(sigData)
	if err != nil {
		log.Printf("[webhook] Failed to marshal canonical payload: %v", err)
		return ""
	}

	log.Printf("[webhook] Canonical payload for HMAC: %s", string(canonical))

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyWebhookSignature(rawBody []byte, signature, secret string) bool {
	computed := computeWebhookHMAC(rawBody, secret)
	if computed == "" {
		return false
	}

	log.Printf("[webhook] Computed HMAC: %s", computed)
	log.Printf("[webhook] Provided HMAC: %s", signature)

	return computed == signature
}

func SignWebhookPayload(rawBody []byte, secret string) string {
	return computeWebhookHMAC(rawBody, secret)
}

func VerifyWebhookSignatureRaw(rawBody []byte, signature, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	computed := hex.EncodeToString(mac.Sum(nil))
	return computed == signature
}

func SignWebhookPayloadRaw(rawBody []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	return hex.EncodeToString(mac.Sum(nil))
}
