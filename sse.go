package pigo

import (
	"bufio"
	"io"
	"strings"
)

func readSSEStream(reader io.Reader, onEvent func(eventName string, data string) (bool, error)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		eventName string
		dataLines []string
	)

	dispatch := func() (bool, error) {
		if len(dataLines) == 0 {
			eventName = ""
			return false, nil
		}

		stop, err := onEvent(eventName, strings.Join(dataLines, "\n"))
		eventName = ""
		dataLines = nil
		return stop, err
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			stop, err := dispatch()
			if err != nil || stop {
				return err
			}
			continue
		}

		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	_, err := dispatch()
	return err
}
