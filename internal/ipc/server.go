package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
)

type HandlerFunc func(ctx context.Context, params json.RawMessage) (interface{}, error)

type StreamHandlerFunc func(ctx context.Context, params json.RawMessage, send func(interface{}) error) error

type Server struct {
	mu             sync.RWMutex
	handlers       map[string]HandlerFunc
	streamHandlers map[string]StreamHandlerFunc
	listener       net.Listener
	wg             sync.WaitGroup
	done           chan struct{}
	closeOnce      sync.Once
}

func NewServer() *Server {
	return &Server{
		handlers:       make(map[string]HandlerFunc),
		streamHandlers: make(map[string]StreamHandlerFunc),
		done:           make(chan struct{}),
	}
}

func (s *Server) Handle(method string, fn HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = fn
}

func (s *Server) HandleStream(method string, fn StreamHandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamHandlers[method] = fn
}

func (s *Server) Serve(socketPath string) error {
	ln, err := listen(socketPath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	go func() {
		<-s.done
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				s.wg.Wait()
				return nil
			default:
				if errors.Is(err, net.ErrClosed) {
					s.Close()
					s.wg.Wait()
					return nil
				}
				continue
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
	})
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	encoder := json.NewEncoder(conn)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = encoder.Encode(NewErrorResponse(0, ErrParseError, "invalid json"))
			continue
		}

		s.mu.RLock()
		streamHandler, isStream := s.streamHandlers[req.Method]
		s.mu.RUnlock()
		if isStream {
			resp, _ := NewResponse(req.ID, map[string]bool{"ok": true})
			if err := encoder.Encode(resp); err != nil {
				return
			}
			send := func(event interface{}) error {
				return encoder.Encode(event)
			}
			_ = streamHandler(ctx, req.Params, send)
			return
		}

		resp := s.dispatch(ctx, req)
		if err := encoder.Encode(resp); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req Request) *Response {
	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()
	if !ok {
		return NewErrorResponse(req.ID, ErrMethodNotFound, "method not found: "+req.Method)
	}

	result, err := handler(ctx, req.Params)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, err.Error())
	}
	resp, err := NewResponse(req.ID, result)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, "failed to marshal result")
	}
	return resp
}
