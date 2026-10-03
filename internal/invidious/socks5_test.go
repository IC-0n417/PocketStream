package invidious

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestSOCKS5DialerSendsRemoteHostname(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	target := make(chan string, 1)
	serverError := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverError <- err
			return
		}
		defer conn.Close()
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			serverError <- err
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			serverError <- err
			return
		}
		header := make([]byte, 5)
		if _, err := io.ReadFull(conn, header); err != nil {
			serverError <- err
			return
		}
		host := make([]byte, int(header[4]))
		if _, err := io.ReadFull(conn, host); err != nil {
			serverError <- err
			return
		}
		portBytes := make([]byte, 2)
		if _, err := io.ReadFull(conn, portBytes); err != nil {
			serverError <- err
			return
		}
		target <- net.JoinHostPort(string(host), strconv.Itoa(int(binary.BigEndian.Uint16(portBytes))))
		_, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		serverError <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialSOCKS5(ctx, listener.Addr().String(), "tcp", "video.example:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if got := <-target; got != "video.example:443" {
		t.Fatalf("target = %q", got)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5DialerUsesSecureCachedAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	target := make(chan string, 1)
	serverError := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverError <- err
			return
		}
		defer conn.Close()
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			serverError <- err
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			serverError <- err
			return
		}
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			serverError <- err
			return
		}
		if header[3] != 1 {
			serverError <- errors.New("SOCKS target was not IPv4")
			return
		}
		addressAndPort := make([]byte, 6)
		if _, err := io.ReadFull(conn, addressAndPort); err != nil {
			serverError <- err
			return
		}
		target <- net.JoinHostPort(net.IP(addressAndPort[:4]).String(), strconv.Itoa(int(binary.BigEndian.Uint16(addressAndPort[4:]))))
		_, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		serverError <- err
	}()

	client := &Client{}
	client.cacheDNSAddresses("www.youtube.com", []net.IPAddr{{IP: net.ParseIP("142.250.74.14")}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := client.socks5DialContext(listener.Addr().String())(ctx, "tcp", "www.youtube.com:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if got := <-target; got != "142.250.74.14:443" {
		t.Fatalf("target = %q", got)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}
