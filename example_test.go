package chirp_test

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
)

func Example() {
	// Server: This peer exports a "strlen" method that returns the length of
	// its argument as a decimal-coded integer.
	//
	// Method handlers can be added to or removed from a peer at any time,
	// including while it is running.
	p := chirp.NewPeer().
		Handle("strlen", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			return []byte(strconv.Itoa(len(req.Data))), nil
		})

	// Set up a Unix-domain socket to receive peer connections.
	lst, err := net.Listen("unix", "example.sock")
	if err != nil {
		log.Fatal("Listen", err)
	}
	go func() {
		conn, err := lst.Accept()
		if err != nil {
			log.Fatal("Accept", err)
		}
		lst.Close()
		p.Start(channel.IO(conn, conn))
	}()

	// Connect to the server socket and set up a peer.
	conn, err := net.Dial("unix", "example.sock")
	if err != nil {
		log.Fatal("Dial", err)
	}

	// Client: This peer does not export any methods of its own, it is used to
	// send a request to the "server" peer established above.
	q := chirp.NewPeer().Start(channel.IO(conn, conn))
	rsp, err := q.Call(context.Background(), "strlen", []byte("hello, world"))
	if err != nil {
		log.Fatal("Call", err)
	}

	// Cleanup: Stop the client and wait for the peers to exit.
	q.Stop()
	p.Wait()

	fmt.Println(string(rsp.Data))
	// Output:
	// 12
}
