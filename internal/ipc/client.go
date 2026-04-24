package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

type Client struct {
	conn    net.Conn
	encoder *json.Encoder
	scanner *bufio.Scanner
	mu      sync.Mutex
}

func Dial(socketPath string) (*Client, error) {
	conn, err := dial(socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial ipc: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	return &Client{
		conn:    conn,
		encoder: json.NewEncoder(conn),
		scanner: scanner,
	}, nil
}

func (c *Client) Call(method string, params interface{}, result interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	req, err := NewRequest(method, params)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	if err := c.encoder.Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer c.conn.SetReadDeadline(time.Time{})

	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		return fmt.Errorf("read response: connection closed")
	}

	var resp Response
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && resp.Result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("unmarshal result: %w", err)
		}
	}
	return nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

func Subscribe(socketPath string, params *SubscribeParams) (<-chan Event, func(), error) {
	conn, err := dial(socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("dial ipc: %w", err)
	}
	encoder := json.NewEncoder(conn)
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	req, err := NewRequest(MethodSubscribe, params)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	if err := encoder.Encode(req); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("send request: %w", err)
	}

	if !scanner.Scan() {
		conn.Close()
		if err := scanner.Err(); err != nil {
			return nil, nil, fmt.Errorf("read response: %w", err)
		}
		return nil, nil, fmt.Errorf("read response: connection closed")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("parse response: %w", err)
	}
	if resp.Error != nil {
		conn.Close()
		return nil, nil, resp.Error
	}

	ch := make(chan Event, 64)
	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			conn.Close()
		})
	}

	go func() {
		defer close(ch)
		for scanner.Scan() {
			var event Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				continue
			}
			select {
			case ch <- event:
			case <-done:
				return
			}
		}
	}()

	return ch, cancel, nil
}
