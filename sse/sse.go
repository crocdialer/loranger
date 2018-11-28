package sse

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/crocdialer/loranger/nodes"
)

// A Server holds open client connections,
// listens for incoming events on its NodeEvent and NodeCommandEvent channels
// and broadcasts event data to all registered connections
type Server struct {
	NodeEvent chan *nodes.Node

	NodeCommandEvent chan []*nodes.NodeCommand

	// Events are pushed to this channel by the main events-gathering routine
	notifier chan []byte

	// New client connections
	newClients chan chan []byte

	// Closed client connections
	closingClients chan chan []byte

	// Client connections registry
	clients map[chan []byte]bool
}

// NewServer creates a new server instance
func NewServer() (server *Server) {
	// Instantiate a server
	server = &Server{
		NodeEvent:        make(chan *nodes.Node),
		NodeCommandEvent: make(chan []*nodes.NodeCommand),
		notifier:         make(chan []byte, 1),
		newClients:       make(chan chan []byte),
		closingClients:   make(chan chan []byte),
		clients:          make(map[chan []byte]bool),
	}

	// Set it running - listening and broadcasting events
	go server.listen()
	return
}

// NumClients returns the number of connected clients
func (server *Server) NumClients() int {
	return len(server.clients)
}

// Listen on different channels and act accordingly
func (server *Server) listen() {
	// eventID := 0

	for {
		select {
		case s := <-server.newClients:
			// new client has connected, register their message channel
			server.clients[s] = true
			// log.Printf("Client added. %d registered clients", len(server.clients))

		case s := <-server.closingClients:
			// client has dettached, stop sending them messages.
			delete(server.clients, s)
			// log.Printf("Removed client. %d registered clients", len(server.clients))

		case node := <-server.NodeEvent:
			if jsonNode, err := json.Marshal(node); err == nil {
				sseBLob := fmt.Sprintf("event: node\ndata: %s\n\n", jsonNode)

				// send out NodeEvent
				server.notifier <- []byte(sseBLob)
			}
		case commandList := <-server.NodeCommandEvent:
			if jsonNode, err := json.Marshal(commandList); err == nil {
				sseBLob := fmt.Sprintf("event: commands\ndata: %s\n\n", jsonNode)

				// send out NodeCommandEvent
				server.notifier <- []byte(sseBLob)
			}
		case event := <-server.notifier:
			// Send event to all connected clients
			for clientMessageChan := range server.clients {
				clientMessageChan <- event
			}
		}
	}
}

func (server *Server) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// make sure that the writer supports flushing.
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

	// each connection registers its own message channel with the server's connections registry
	messageChan := make(chan []byte)

	// signal the server that we have a new connection
	server.newClients <- messageChan

	// remove client from map of connected clients when this handler exits.
	defer func() {
		server.closingClients <- messageChan
	}()

	// Listen to connection close and un-register messageChan
	notifyClose := rw.(http.CloseNotifier).CloseNotify()
	running := true

	// block waiting for messages broadcast on this connection's messageChan
	for running {
		select {
		case msg := <-messageChan:
			rw.Write(msg)
			flusher.Flush()
		case <-notifyClose:
			running = false
		}

	}
}
