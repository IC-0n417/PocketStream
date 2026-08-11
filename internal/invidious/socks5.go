package invidious

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

func socks5DialContext(proxyAddress string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialSOCKS5(ctx, proxyAddress, network, address)
	}
}

func dialSOCKS5(ctx context.Context, proxyAddress, network, targetAddress string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("SOCKS5 only supports TCP, got %q", network)
	}
	host, portText, err := net.SplitHostPort(targetAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid SOCKS5 target: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid SOCKS5 target port")
	}

	dialer := &net.Dialer{Timeout: 6 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, fmt.Errorf("connect to network proxy: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = conn.Close()
		}
	}()

	deadline := time.Now().Add(6 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := writeAll(conn, []byte{5, 1, 0}); err != nil {
		return nil, err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return nil, fmt.Errorf("read SOCKS5 greeting: %w", err)
	}
	if greeting[0] != 5 || greeting[1] != 0 {
		return nil, fmt.Errorf("network proxy rejected authentication method: %v", greeting)
	}

	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 1)
			request = append(request, ipv4...)
		} else {
			request = append(request, 4)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, errors.New("invalid SOCKS5 target hostname")
		}
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	request = append(request, portBytes...)
	if err := writeAll(conn, request); err != nil {
		return nil, err
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return nil, fmt.Errorf("read SOCKS5 response: %w", err)
	}
	if reply[0] != 5 || reply[1] != 0 {
		return nil, fmt.Errorf("network proxy connect failed with code %d", reply[1])
	}
	if err := discardSOCKS5Address(conn, reply[3]); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	failed = false
	return conn, nil
}

func discardSOCKS5Address(reader io.Reader, addressType byte) error {
	length := 0
	switch addressType {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		value := []byte{0}
		if _, err := io.ReadFull(reader, value); err != nil {
			return fmt.Errorf("read SOCKS5 address length: %w", err)
		}
		length = int(value[0])
	default:
		return fmt.Errorf("invalid SOCKS5 address type %d", addressType)
	}
	buffer := make([]byte, length+2)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return fmt.Errorf("read SOCKS5 bound address: %w", err)
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return fmt.Errorf("write SOCKS5 request: %w", err)
		}
		data = data[written:]
	}
	return nil
}
