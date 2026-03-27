package events

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// EventBroker manages client connections and broadcasts events.
type EventBroker struct {
	clients   map[chan []byte]bool
	broadcast chan []byte
	quit      chan struct{}
	logger    *slog.Logger
	mu        sync.RWMutex
}

// NewEventBroker creates a new EventBroker.
func NewEventBroker(logger *slog.Logger) *EventBroker {
	return &EventBroker{
		clients:   make(map[chan []byte]bool),
		broadcast: make(chan []byte, 1),
		quit:      make(chan struct{}),
		logger:    logger,
	}
}

// Start begins the event broker's operation. This method should be run in a goroutine.
func (broker *EventBroker) Start() {
	for {
		select {
		case event := <-broker.broadcast:
			broker.mu.RLock()
			for clientChan := range broker.clients {
				select {
				case clientChan <- event:
				default:
					broker.logger.Warn("Skipping event for slow client", "client", clientChan)
				}
			}
			broker.mu.RUnlock()
		case <-broker.quit:
			broker.mu.Lock()
			for clientChan := range broker.clients {
				close(clientChan)
			}
			broker.clients = make(map[chan []byte]bool)
			broker.mu.Unlock()
			broker.logger.Info("EventBroker stopped.")
			return
		}
	}
}

// Stop terminates the event broker.
func (broker *EventBroker) Stop() {
	broker.logger.Info("Stopping EventBroker...")
	close(broker.quit)
}

// Subscribe allows a client to receive events. Client must provide a buffered channel.
func (broker *EventBroker) Subscribe(clientChan chan []byte) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.clients[clientChan] = true
	broker.logger.Info("Client subscribed to EventBroker")
}

// Unsubscribe removes a client from receiving events.
func (broker *EventBroker) Unsubscribe(clientChan chan []byte) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if _, ok := broker.clients[clientChan]; ok {
		delete(broker.clients, clientChan)
		broker.logger.Info("Client unsubscribed from EventBroker")
	}
}

// Broadcast sends an event to all subscribed clients after marshaling it to JSON.
func (broker *EventBroker) Broadcast(data interface{}) {
	payload, err := json.Marshal(data)
	if err != nil {
		broker.logger.Error("Failed to marshal event data to JSON for broadcast", "error", err)
		return
	}
	broker.broadcast <- payload
}
