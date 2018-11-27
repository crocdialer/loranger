package broker

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/crocdialer/loranger/nodes"
)

// A Broker holds open client connections,
// listens for incoming events on its Notifier channel
// and broadcast event data to all registered connections
type Broker struct {
	NodeMsg chan *nodes.Node

	// Events are pushed to this channel by the main events-gathering routine
	notifier chan []byte

	// New client connections
	newClients chan chan []byte

	// Closed client connections
	closingClients chan chan []byte

	// Client connections registry
	clients map[chan []byte]bool
}

// NewServer creates a new Broker instance
func NewServer() (broker *Broker) {
	// Instantiate a broker
	broker = &Broker{
		NodeMsg:        make(chan *nodes.Node),
		notifier:       make(chan []byte, 1),
		newClients:     make(chan chan []byte),
		closingClients: make(chan chan []byte),
		clients:        make(map[chan []byte]bool),
	}

	// Set it running - listening and broadcasting events
	go broker.listen()
	return
}

// Listen on different channels and act accordingly
func (broker *Broker) listen() {
	// eventID := 0

	for {
		select {
		case s := <-broker.newClients:
			// new client has connected, register their message channel
			broker.clients[s] = true
			// log.Printf("Client added. %d registered clients", len(broker.clients))

		case s := <-broker.closingClients:
			// client has dettached, stop sending them messages.
			delete(broker.clients, s)
			// log.Printf("Removed client. %d registered clients", len(broker.clients))

		case node := <-broker.NodeMsg:
			if jsonNode, err := json.Marshal(node); err == nil {
				sseBLob := fmt.Sprintf("event: node\ndata: %s\n\n", jsonNode)

				// send out NodeEvent
				broker.notifier <- []byte(sseBLob)
			}

		case event := <-broker.notifier:

			// Send event to all connected clients
			for clientMessageChan := range broker.clients {
				clientMessageChan <- event
			}
		}
	}
}

func (broker *Broker) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Make sure that the writer supports flushing.
	flusher, ok := rw.(http.Flusher)

	if !ok {
		http.Error(rw, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	// Set the headers related to event streaming.
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")
	rw.Header().Set("Access-Control-Allow-Origin", "*")

	// Each connection registers its own message channel with the Broker's connections registry
	messageChan := make(chan []byte)

	// Signal the broker that we have a new connection
	broker.newClients <- messageChan

	// Remove this client from the map of connected clients
	// when this handler exits.
	defer func() {
		broker.closingClients <- messageChan
	}()

	// Listen to connection close and un-register messageChan
	notify := rw.(http.CloseNotifier).CloseNotify()

	go func() {
		<-notify
		broker.closingClients <- messageChan
	}()

	// block waiting for messages broadcast on this connection's messageChan
	for {
		rw.Write(<-messageChan)
		flusher.Flush()
	}
}
