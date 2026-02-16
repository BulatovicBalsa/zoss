package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Handlers struct {
	store *OrderStore
	sm    *StateMachine
}

func NewHandlers(store *OrderStore, sm *StateMachine) *Handlers {
	return &Handlers{store: store, sm: sm}
}

// CreateOrder handles POST /orders
// Creates a new order in PENDING_PAYMENT state.
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

// GetOrder handles GET /orders/{orderID}
// Returns the current state of an order.
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

// PayOrder handles POST /orders/{orderID}/pay
// Simulates a payment success event: transitions order from PENDING_PAYMENT → PAID.
//
// In the real system this transition would be triggered by a Kafka event from the
// Payment service. Here it is exposed as an HTTP endpoint for demonstration purposes.
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

	// Record payment ID on the order
	_ = h.store.UpdateOrderPaymentID(r.Context(), orderID, req.PaymentID)

	log.Printf("[handler] Order %s marked as PAID (payment_id=%s)", orderID, req.PaymentID)
	writeJSON(w, http.StatusOK, map[string]string{
		"order_id": orderID,
		"status":   StatusPaid,
		"message":  "payment accepted",
	})
}

// CancelOrder handles POST /orders/{orderID}/cancel
// Customer cancels an order: transitions from PENDING_PAYMENT → CANCELLED.
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

// GetOrderHistory handles GET /orders/{orderID}/history
// Returns the full status change history for an order.
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

// writeJSON serializes v as JSON and writes it to the response.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[handler] Failed to encode JSON response: %v", err)
	}
}
