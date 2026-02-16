package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

type Handlers struct {
	store         *OrderStore
	sm            *StateMachine
	webhookSecret string
}

func NewHandlers(store *OrderStore, sm *StateMachine, webhookSecret string) *Handlers {
	return &Handlers{store: store, sm: sm, webhookSecret: webhookSecret}
}

func (h *Handlers) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.CustomerID == "" || len(req.Items) == 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "customer_id and items are required"})
		return
	}

	order, err := h.store.CreateOrder(r.Context(), req)
	if err != nil {
		log.Printf("[handler] CreateOrder error: %v", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create order"})
		return
	}

	log.Printf("[handler] Created order %s for customer %s (total=%.2f)",
		order.OrderID, order.CustomerID, order.Total)

	writeJSON(w, http.StatusCreated, order)
}

func (h *Handlers) GetOrder(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "orderID")

	order, err := h.store.GetOrder(r.Context(), orderID)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "order not found"})
			return
		}
		log.Printf("[handler] GetOrder error: %v", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to get order"})
		return
	}

	writeJSON(w, http.StatusOK, order)
}

func (h *Handlers) PayOrder(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "orderID")

	var req PayOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	err := h.sm.Transition(r.Context(), orderID, StatusPaid, "payment confirmed: "+req.PaymentID)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "order not found"})
			return
		}
		if errors.Is(err, ErrTransitionNotAllowed) || errors.Is(err, ErrTransitionConflict) || errors.Is(err, ErrLockNotAcquired) || errors.Is(err, ErrLockExpired) {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error()})
			return
		}
		log.Printf("[handler] PayOrder error: %v", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to process payment"})
		return
	}

	_ = h.store.UpdateOrderPaymentID(r.Context(), orderID, req.PaymentID)

	log.Printf("[handler] Order %s marked as PAID (payment_id=%s)", orderID, req.PaymentID)
	writeJSON(w, http.StatusOK, map[string]string{
		"order_id": orderID,
		"status":   StatusPaid,
		"message":  "payment accepted",
	})
}

func (h *Handlers) CancelOrder(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "orderID")

	var req CancelOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	reason := req.Reason
	if reason == "" {
		reason = "cancelled by customer"
	}

	err := h.sm.Transition(r.Context(), orderID, StatusCancelled, reason)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "order not found"})
			return
		}
		if errors.Is(err, ErrTransitionNotAllowed) || errors.Is(err, ErrTransitionConflict) || errors.Is(err, ErrLockNotAcquired) || errors.Is(err, ErrLockExpired) {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error()})
			return
		}
		log.Printf("[handler] CancelOrder error: %v", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to cancel order"})
		return
	}

	log.Printf("[handler] Order %s CANCELLED (reason: %s)", orderID, reason)
	writeJSON(w, http.StatusOK, map[string]string{
		"order_id": orderID,
		"status":   StatusCancelled,
		"message":  "order cancelled",
	})
}

func (h *Handlers) ShipOrder(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "orderID")

	err := h.sm.Transition(r.Context(), orderID, StatusShipping, "shipment initiated")
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "order not found"})
			return
		}
		if errors.Is(err, ErrTransitionNotAllowed) || errors.Is(err, ErrTransitionConflict) || errors.Is(err, ErrLockNotAcquired) || errors.Is(err, ErrLockExpired) {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error()})
			return
		}
		log.Printf("[handler] ShipOrder error: %v", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to initiate shipping"})
		return
	}

	log.Printf("[handler] Order %s marked as SHIPPING", orderID)
	writeJSON(w, http.StatusOK, map[string]string{
		"order_id": orderID,
		"status":   StatusShipping,
		"message":  "shipping initiated",
	})
}

func (h *Handlers) ShippingWebhook(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[webhook] Failed to read request body: %v", err)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "failed to read body"})
		return
	}
	defer r.Body.Close()

	signature := r.Header.Get("X-Webhook-Signature")
	if signature == "" {
		log.Printf("[webhook] Missing X-Webhook-Signature header")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "missing signature header"})
		return
	}

	if !VerifyWebhookSignature(rawBody, signature, h.webhookSecret) {
		log.Printf("[webhook] Signature verification FAILED")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid webhook signature"})
		return
	}

	log.Printf("[webhook] Signature verification PASSED")

	var event ShippingWebhookEvent
	if err := json.Unmarshal(rawBody, &event); err != nil {
		log.Printf("[webhook] Failed to parse webhook event: %v", err)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid event payload"})
		return
	}

	log.Printf("[webhook] Received event: shipment=%s order=%s type=%s status=%s",
		event.ShipmentID, event.OrderID, event.EventType, event.Status)

	if event.OrderID == "" || event.ShipmentID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "order_id and shipment_id are required"})
		return
	}

	order, err := h.store.GetOrder(r.Context(), event.OrderID)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "order not found"})
			return
		}
		log.Printf("[webhook] GetOrder error: %v", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to get order"})
		return
	}

	if order.Status != StatusShipping {
		log.Printf("[webhook] Order %s is not in SHIPPING state (current=%s), ignoring", event.OrderID, order.Status)
		writeJSON(w, http.StatusConflict, ErrorResponse{
			Error: fmt.Sprintf("order is in %s state, expected SHIPPING", order.Status),
		})
		return
	}

	var newStatus, reason string
	var refundTriggered bool

	switch event.Status {
	case ShipStatusDelivered:
		newStatus = StatusDelivered
		reason = fmt.Sprintf("delivered — confirmed by webhook (shipment %s)", event.ShipmentID)

	case ShipStatusLost, ShipStatusDamaged:
		newStatus = StatusShipFailed
		reason = fmt.Sprintf("shipment %s — refund initiated (shipment %s)",
			strings.ToLower(event.Status), event.ShipmentID)
		refundTriggered = true
		log.Printf("[webhook] *** REFUND TRIGGERED for order %s (shipment %s, reason: %s) ***",
			event.OrderID, event.ShipmentID, event.Status)

	case ShipStatusInTransit:
		log.Printf("[webhook] Order %s: shipment %s is in transit", event.OrderID, event.ShipmentID)
		writeJSON(w, http.StatusOK, ShippingWebhookResponse{
			OrderID:         event.OrderID,
			ShipmentID:      event.ShipmentID,
			PreviousStatus:  order.Status,
			NewStatus:       order.Status,
			RefundTriggered: false,
			Message:         "status noted, no state change",
		})
		return

	case ShipStatusReturned:
		newStatus = StatusShipFailed
		reason = fmt.Sprintf("shipment returned (shipment %s)", event.ShipmentID)

	default:
		log.Printf("[webhook] Unknown shipping status: %s", event.Status)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: fmt.Sprintf("unknown shipping status: %s", event.Status),
		})
		return
	}

	err = h.store.UpdateOrderStatus(r.Context(), event.OrderID, newStatus, reason)
	if err != nil {
		log.Printf("[webhook] UpdateOrderStatus error: %v", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update order status"})
		return
	}

	log.Printf("[webhook] Order %s: %s → %s (refund=%v)", event.OrderID, order.Status, newStatus, refundTriggered)

	writeJSON(w, http.StatusOK, ShippingWebhookResponse{
		OrderID:         event.OrderID,
		ShipmentID:      event.ShipmentID,
		PreviousStatus:  order.Status,
		NewStatus:       newStatus,
		RefundTriggered: refundTriggered,
		Message:         fmt.Sprintf("order transitioned to %s", newStatus),
	})
}

func (h *Handlers) GetOrderHistory(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "orderID")

	history, err := h.store.GetOrderHistory(r.Context(), orderID)
	if err != nil {
		log.Printf("[handler] GetOrderHistory error: %v", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to get history"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"order_id": orderID,
		"history":  history,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[handler] Failed to encode JSON response: %v", err)
	}
}