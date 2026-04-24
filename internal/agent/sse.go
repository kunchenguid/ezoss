package agent

import (
	"bufio"
	"io"
	"strings"
)

type sseEvent struct {
	Name string
	Data string
}

func parseSSE(r io.Reader, handler func(sseEvent) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024*1024)
	var name string
	var dataLines []string

	flush := func() bool {
		if len(dataLines) == 0 && name == "" {
			return true
		}
		data := strings.Join(dataLines, "\n")
		cont := handler(sseEvent{Name: name, Data: data})
		name = ""
		dataLines = nil
		return cont
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			name = strings.TrimPrefix(line[6:], " ")
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(line[5:], " "))
		}
	}

	flush()
	return scanner.Err()
}
