package main

import (
	"errors"
	"time"
)

const (
	StatusPendingPayment = "PENDING_PAYMENT"
	StatusPaid           = "PAID"
	StatusCancelled      = "CANCELLED"
	StatusShipping       = "SHIPPING"
	StatusDelivered      = "DELIVERED"
	StatusShipFailed     = "SHIP_FAILED"
)

var (
	ErrOrderNotFound        = errors.New("order not found")
	ErrTransitionNotAllowed = errors.New("state transition not allowed")
	ErrLockNotAcquired      = errors.New("could not acquire distributed lock")
	ErrLockExpired          = errors.New("lock expired or stolen during processing (ownership lost)")
	ErrTransitionConflict   = errors.New("state changed by another process")
)

type OrderItem struct {
	ProductID string  `json:"product_id"`
	Quantity  int     `json:"quantity"`
	Price     float64 `json:"price"`
}

type Order struct {
	OrderID    string      `json:"order_id"`
	CustomerID string      `json:"customer_id"`
	Status     string      `json:"status"`
	Items      []OrderItem `json:"items"`
	Total      float64     `json:"total"`
	PaymentID  string      `json:"payment_id,omitempty"`
	Reason     string      `json:"reason,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

type StatusChange struct {
	OrderID   string    `json:"order_id"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	ChangedAt time.Time `json:"changed_at"`
}

type CreateOrderRequest struct {
	CustomerID string      `json:"customer_id"`
	Items      []OrderItem `json:"items"`
}

type PayOrderRequest struct {
	PaymentID string `json:"payment_id"`
}

type CancelOrderRequest struct {
	Reason string `json:"reason"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type ShippingWebhookEvent struct {
	ShipmentID string `json:"shipment_id"`
	OrderID    string `json:"order_id"`
	EventType  string `json:"event_type"`
	Status     string `json:"status"`
	Details    string `json:"details,omitempty"`
	Timestamp  int64  `json:"timestamp"`
}

type ShippingWebhookResponse struct {
	OrderID         string `json:"order_id"`
	ShipmentID      string `json:"shipment_id"`
	PreviousStatus  string `json:"previous_status"`
	NewStatus       string `json:"new_status"`
	RefundTriggered bool   `json:"refund_triggered"`
	Message         string `json:"message"`
}

const (
	ShipStatusInTransit = "IN_TRANSIT"
	ShipStatusDelivered = "DELIVERED"
	ShipStatusLost      = "LOST"
	ShipStatusDamaged   = "DAMAGED"
	ShipStatusReturned  = "RETURNED"
)