package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"
)

var AllowedTransitions = map[string]map[string]bool{
	StatusPendingPayment: {StatusPaid: true, StatusCancelled: true},
	StatusPaid:           {StatusShipping: true},
	StatusShipping:       {StatusDelivered: true, StatusShipFailed: true},
}

type StateMachine struct {
	store              *OrderStore
	rdb                *redis.Client
	lockTTL            time.Duration
	maxProcessingDelay time.Duration
}

func NewStateMachine(store *OrderStore, rdb *redis.Client, lockTTL, maxProcessingDelay time.Duration) *StateMachine {
	return &StateMachine{
		store:              store,
		rdb:                rdb,
		lockTTL:            lockTTL,
		maxProcessingDelay: maxProcessingDelay,
	}
}

func (sm *StateMachine) Transition(ctx context.Context, orderID, targetState, reason string) error {
	lockKey := fmt.Sprintf("order_lock:%s", orderID)

	var acquired bool
	for retries := 0; retries < 50; retries++ {
		var err error
		acquired, err = sm.rdb.SetNX(ctx, lockKey, "1", sm.lockTTL).Result()
		if err != nil {
			return fmt.Errorf("redis lock error: %w", err)
		}
		if acquired {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !acquired {
		return ErrLockNotAcquired
	}

	log.Printf("[state] Order %s: lock acquired (value=\"1\", TTL=%v)", orderID, sm.lockTTL)

	defer func() {
		sm.rdb.Del(ctx, lockKey)
		log.Printf("[state] Order %s: lock released (DEL)", orderID)
	}()

	order, err := sm.store.GetOrder(ctx, orderID)
	if err != nil {
		return err
	}
	currentState := order.Status

	if !isAllowed(currentState, targetState) {
		return ErrTransitionNotAllowed
	}

	if sm.maxProcessingDelay > 0 {
		delay := time.Duration(rand.Int63n(int64(sm.maxProcessingDelay)))
		log.Printf("[state] Order %s: processing (%v delay)...", orderID, delay)
		time.Sleep(delay)
	}

	err = sm.store.UpdateOrderStatus(ctx, orderID, targetState, reason)
	if err != nil {
		return err
	}

	log.Printf("[state] Order %s: %s → %s COMMITTED", orderID, currentState, targetState)
	return nil
}

func isAllowed(currentState, targetState string) bool {
	targets, ok := AllowedTransitions[currentState]
	if !ok {
		return false
	}
	return targets[targetState]
}