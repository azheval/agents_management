package events_test

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"agent-management/server/internal/events"
)

// setupTestBroker creates a new broker with a discard logger for tests.
func setupTestBroker() *events.EventBroker {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	broker := events.NewEventBroker(logger)
	go broker.Start()
	return broker
}


// TestEventBroker_Subscribe tests that a client can subscribe to the broker.
func TestEventBroker_Subscribe(t *testing.T) {
	broker := setupTestBroker()
	defer broker.Stop()

	clientChan := make(chan []byte, 1)
	broker.Subscribe(clientChan)

	testEvent := map[string]string{"message": "test event"}
	broker.Broadcast(testEvent)

	select {
	case receivedEvent := <-clientChan:
		// Note: The event is JSON encoded by the broker
		expectedJSON := `{"message":"test event"}`
		if string(receivedEvent) != expectedJSON {
			t.Errorf("Expected event %s, got %s", expectedJSON, string(receivedEvent))
		}
	case <-time.After(time.Second):
		t.Fatal("Client did not receive event within timeout")
	}
}

// TestEventBroker_Publish tests that a client receives a message broadcast by the broker.
func TestEventBroker_Publish(t *testing.T) {
	broker := setupTestBroker()
	defer broker.Stop()

	client1Chan := make(chan []byte, 1)
	client2Chan := make(chan []byte, 1)
	broker.Subscribe(client1Chan)
	broker.Subscribe(client2Chan)

	testEvent := map[string]string{"message": "broadcast message"}
	broker.Broadcast(testEvent)

	expectedJSON := `{"message":"broadcast message"}`

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		select {
		case receivedEvent := <-client1Chan:
			if string(receivedEvent) != expectedJSON {
				t.Errorf("Client 1: Expected event %s, got %s", expectedJSON, string(receivedEvent))
			}
		case <-time.After(time.Second):
			t.Error("Client 1 did not receive event within timeout")
		}
	}()

	go func() {
		defer wg.Done()
		select {
		case receivedEvent := <-client2Chan:
			if string(receivedEvent) != expectedJSON {
				t.Errorf("Client 2: Expected event %s, got %s", expectedJSON, string(receivedEvent))
			}
		case <-time.After(time.Second):
			t.Error("Client 2 did not receive event within timeout")
		}
	}()

	wg.Wait()
}

// TestEventBroker_Unsubscribe tests that a client who has disconnected no longer receives messages.
func TestEventBroker_Unsubscribe(t *testing.T) {
	broker := setupTestBroker()
	defer broker.Stop()

	clientChan := make(chan []byte, 1)
	broker.Subscribe(clientChan)

	// Publish an event to ensure the client is connected
	broker.Broadcast(map[string]string{"message": "initial event"})
	select {
	case <-clientChan:
		// Event received, client is active
	case <-time.After(time.Second):
		t.Fatal("Client did not receive initial event")
	}

	broker.Unsubscribe(clientChan)

	// Publish another event
	broker.Broadcast(map[string]string{"message": "event after unsubscribe"})

	select {
	case <-clientChan:
		t.Error("Client received event after unsubscribing")
	case <-time.After(200 * time.Millisecond): // Short timeout to confirm no event is received
		// Expected: no event received
	}
}
