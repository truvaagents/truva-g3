package openaiwire

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errSSEEventTooLarge = errors.New("OpenAI wire SSE event exceeds configured byte limit")

type sseEvent struct {
	Data []byte
	Done bool
}

type sseParser struct {
	reader        *bufio.Reader
	maxEventBytes int
	eof           bool
}

func newSSEParser(reader io.Reader, maxEventBytes int) *sseParser {
	return &sseParser{
		reader:        bufio.NewReader(reader),
		maxEventBytes: maxEventBytes,
	}
}

func (p *sseParser) Next() (sseEvent, error) {
	if p == nil || p.reader == nil {
		return sseEvent{}, errors.New("OpenAI wire SSE reader is nil")
	}
	if p.eof {
		return sseEvent{}, io.EOF
	}

	var data bytes.Buffer
	eventBytes := 0
	for {
		line, bytesRead, reachedEOF, err := p.readLine(p.maxEventBytes - eventBytes)
		eventBytes += bytesRead
		if err != nil {
			return sseEvent{}, err
		}
		if reachedEOF {
			p.eof = true
		}

		if len(line) == 0 {
			if data.Len() > 0 {
				return makeSSEEvent(data.Bytes()), nil
			}
			if reachedEOF {
				return sseEvent{}, io.EOF
			}
			eventBytes = 0
			continue
		}
		if line[0] != ':' {
			field, value, found := bytes.Cut(line, []byte{':'})
			if !found {
				field = line
				value = nil
			}
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			if bytes.Equal(field, []byte("data")) {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				_, _ = data.Write(value)
			}
		}
		if reachedEOF {
			if data.Len() == 0 {
				return sseEvent{}, io.EOF
			}
			return makeSSEEvent(data.Bytes()), nil
		}
	}
}

func (p *sseParser) readLine(remaining int) ([]byte, int, bool, error) {
	if remaining <= 0 {
		return nil, 0, false, errSSEEventTooLarge
	}
	line := make([]byte, 0, min(remaining, 4096))
	read := 0
	for {
		fragment, err := p.reader.ReadSlice('\n')
		read += len(fragment)
		if read > remaining {
			return nil, read, false, errSSEEventTooLarge
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			return trimSSELineEnding(line), read, false, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return trimSSELineEnding(line), read, true, nil
		default:
			return nil, read, false, fmt.Errorf("read OpenAI wire SSE event: %w", err)
		}
	}
}

func trimSSELineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return line
}

func makeSSEEvent(data []byte) sseEvent {
	cloned := append([]byte(nil), data...)
	return sseEvent{
		Data: cloned,
		Done: strings.TrimSpace(string(cloned)) == "[DONE]",
	}
}
