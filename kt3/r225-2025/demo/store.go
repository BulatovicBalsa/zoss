package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

type OrderStore struct {
	session *gocql.Session
}

func NewOrderStore(session *gocql.Session) *OrderStore {
	return &OrderStore{session: session}
}

// InitSchema creates the keyspace and tables if they don't exist.
// Called once at startup after Cassandra connection is established.
func (s *OrderStore) InitSchema() error {
	log.Println("[store] Initializing Cassandra schema...")

	err := s.session.Query(`
		CREATE KEYSPACE IF NOT EXISTS ordering
		WITH replication = {
			'class': 'SimpleStrategy',
			'replication_factor': 1
		}
	`).Exec()
	if err != nil {
		return fmt.Errorf("create keyspace: %w", err)
	}

	err = s.session.Query(`
		CREATE TABLE IF NOT EXISTS ordering.orders (
			order_id    TEXT PRIMARY KEY,
			customer_id TEXT,
			status      TEXT,
			items       TEXT,
			total       DOUBLE,
			payment_id  TEXT,
			reason      TEXT,
			created_at  TIMESTAMP,
			updated_at  TIMESTAMP
		)
	`).Exec()
	if err != nil {
		return fmt.Errorf("create orders table: %w", err)
	}

	err = s.session.Query(`
		CREATE TABLE IF NOT EXISTS ordering.order_status_history (
			order_id   TEXT,
			changed_at TIMESTAMP,
			status     TEXT,
			reason     TEXT,
			PRIMARY KEY (order_id, changed_at)
		) WITH CLUSTERING ORDER BY (changed_at DESC)
	`).Exec()
	if err != nil {
		return fmt.Errorf("create order_status_history table: %w", err)
	}

	log.Println("[store] Schema initialized successfully")
	return nil
}

// CreateOrder inserts a new order with PENDING_PAYMENT status.
func (s *OrderStore) CreateOrder(_ context.Context, req CreateOrderRequest) (*Order, error) {
	orderID := uuid.New().String()
	now := time.Now()

	var total float64
	for _, item := range req.Items {
		total += item.Price * float64(item.Quantity)
	}

	// Serialize items as simple JSON string for demo purposes
	itemsJSON := "["
	for i, item := range req.Items {
		if i > 0 {
			itemsJSON += ","
		}
		itemsJSON += fmt.Sprintf(
			`{"product_id":"%s","quantity":%d,"price":%.2f}`,
			item.ProductID, item.Quantity, item.Price,
		)
	}
	itemsJSON += "]"

	err := s.session.Query(`
		INSERT INTO ordering.orders
			(order_id, customer_id, status, items, total, payment_id, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', '', ?, ?)
	`, orderID, req.CustomerID, StatusPendingPayment, itemsJSON, total, now, now).Exec()
	if err != nil {
		return nil, fmt.Errorf("insert order: %w", err)
	}

	// Record initial status in history
	err = s.session.Query(`
		INSERT INTO ordering.order_status_history (order_id, changed_at, status, reason)
		VALUES (?, ?, ?, ?)
	`, orderID, now, StatusPendingPayment, "order created").Exec()
	if err != nil {
		return nil, fmt.Errorf("insert initial status history: %w", err)
	}

	return &Order{
		OrderID:    orderID,
		CustomerID: req.CustomerID,
		Status:     StatusPendingPayment,
		Items:      req.Items,
		Total:      total,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// GetOrder retrieves an order by ID.
func (s *OrderStore) GetOrder(_ context.Context, orderID string) (*Order, error) {
	var order Order
	var itemsJSON string

	err := s.session.Query(`
		SELECT order_id, customer_id, status, items, total, payment_id, reason, created_at, updated_at
		FROM ordering.orders
		WHERE order_id = ?
	`, orderID).Scan(
		&order.OrderID,
		&order.CustomerID,
		&order.Status,
		&itemsJSON,
		&order.Total,
		&order.PaymentID,
		&order.Reason,
		&order.CreatedAt,
		&order.UpdatedAt,
	)
	if err != nil {
		if err == gocql.ErrNotFound {
			return nil, ErrOrderNotFound
		}
		return nil, fmt.Errorf("get order: %w", err)
	}

	return &order, nil
}

// UpdateOrderStatus updates the order status and records the change in history.
func (s *OrderStore) UpdateOrderStatus(_ context.Context, orderID, newStatus, reason string) error {
	now := time.Now()

	err := s.session.Query(`
		UPDATE ordering.orders
		SET status = ?, reason = ?, updated_at = ?
		WHERE order_id = ?
	`, newStatus, reason, now, orderID).Exec()
	if err != nil {
		return fmt.Errorf("update order status: %w", err)
	}

	// Record status change in history
	err = s.session.Query(`
		INSERT INTO ordering.order_status_history (order_id, changed_at, status, reason)
		VALUES (?, ?, ?, ?)
	`, orderID, now, newStatus, reason).Exec()
	if err != nil {
		return fmt.Errorf("insert status history: %w", err)
	}

	return nil
}

// UpdateOrderPaymentID sets the payment_id field on an order.
func (s *OrderStore) UpdateOrderPaymentID(_ context.Context, orderID, paymentID string) error {
	err := s.session.Query(`
		UPDATE ordering.orders SET payment_id = ? WHERE order_id = ?
	`, paymentID, orderID).Exec()
	if err != nil {
		return fmt.Errorf("update payment_id: %w", err)
	}
	return nil
}

// GetOrderHistory returns the status change history for an order,
// ordered by most recent first.
func (s *OrderStore) GetOrderHistory(_ context.Context, orderID string) ([]StatusChange, error) {
	iter := s.session.Query(`
		SELECT order_id, changed_at, status, reason
		FROM ordering.order_status_history
		WHERE order_id = ?
		ORDER BY changed_at DESC
	`, orderID).Iter()

	var history []StatusChange
	var sc StatusChange
	for iter.Scan(&sc.OrderID, &sc.ChangedAt, &sc.Status, &sc.Reason) {
		history = append(history, sc)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("get order history: %w", err)
	}

	return history, nil
}

func ConnectCassandra(host string, timeout time.Duration) (*gocql.Session, error) {
	deadline := time.Now().Add(timeout)

	for {
		cluster := gocql.NewCluster(host)
		cluster.Consistency = gocql.Quorum
		cluster.Timeout = 10 * time.Second
		cluster.ConnectTimeout = 10 * time.Second

		session, err := cluster.CreateSession()
		if err == nil {
			// Verify the connection works
			if err := session.Query("SELECT now() FROM system.local").Exec(); err == nil {
				log.Printf("[store] Connected to Cassandra at %s", host)
				return session, nil
			}
			session.Close()
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("cassandra connection timeout after %v: %w", timeout, err)
		}

		log.Printf("[store] Cassandra not ready, retrying in 5s... (%v)", err)
		time.Sleep(5 * time.Second)
	}
}
