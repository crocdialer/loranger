package main

import (
	"log"
	"os"
	"strings"

	"github.com/tarm/serial"
)

var device string

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
		// message = strings.Replace(message, "\n", "", -1)

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
	log.Println("welcome to loranger application")

	device = os.Args[1]
	log.Println("# Starting Serial Listener on", device)

	c := &serial.Config{Name: device, Baud: 115200}
	s, err := serial.OpenPort(c)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	serial_input := make(chan string, 100)
	go readSerial(s, serial_input)

	for i := range serial_input {
		log.Println(i)
	}
}
