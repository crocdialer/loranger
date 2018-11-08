package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/tarm/serial"
)

var device string
var last_line string

func handler(w http.ResponseWriter, r *http.Request) {
	// fmt.Fprintf(w, "Hi there, I love %s!", r.URL.Path[1:])
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(last_line))
}

func readSerial(s *serial.Port, output chan<- string) {
	buf := make([]byte, 256)
	var message string

	for {
		n, err := s.Read(buf)
		if err != nil {
			log.Fatal(err)
		}

		message += string(buf[:n])
		message = strings.Replace(message, "\r", "", -1)

		lines := strings.Split(message, "\n")

		if len(lines) > 1 {
			message = lines[len(lines)-1]

			for _, l := range lines[:len(lines)-1] {
				// log.Print(fmt.Sprintf("%d: %s", i, l))
				output <- l
			}
		}
	}
}

func main() {
	log.Println("welcome loranger")

	if len(os.Args) > 1 {
		device = os.Args[1]
	}

	// create channel
	serial_input := make(chan string, 100)

	files, err := ioutil.ReadDir("/dev")
	if err != nil {
		log.Fatal(err)
	}

	for _, f := range files {
		device_name := "/dev/" + f.Name()

		if strings.Contains(device_name, "ttyACM") || strings.Contains(device_name, "tty.usb") {
			c := &serial.Config{Name: device_name, Baud: 115200}
			s, err := serial.OpenPort(c)
			if err != nil {
				log.Fatal(err)
				return
			}
			defer s.Close()

			log.Println("reading from", device_name)

			// producer feeds lines into channel
			go readSerial(s, serial_input)
		}
	}

	// consumer
	go func(input chan string) {
		for line := range input {
			log.Println(line)
			last_line = line
		}
	}(serial_input)

	http.HandleFunc("/", handler)

	port := 8080
	log.Println("server listening on", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}
