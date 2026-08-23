package openaiwire

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSSEParserCommentsCRLFMultilineDataAndDone(t *testing.T) {
	parser := newSSEParser(strings.NewReader(
		": keepalive\r\n"+
			"event: message\r\n"+
			"data:{\"value\":\r\n"+
			"data: 1}\r\n\r\n"+
			"data:   [DONE]  \r\n\r\n",
	), DefaultMaxSSEEventBytes)
	event, err := parser.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(event.Data), "{\"value\":\n1}"; got != want || event.Done {
		t.Fatalf("event = %q, done=%t; want %q", got, event.Done, want)
	}
	done, err := parser.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !done.Done {
		t.Fatalf("done event = %#v", done)
	}
	if _, err := parser.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-done error = %v, want EOF", err)
	}
}

func TestSSEParserDefensiveEOFAndBareFieldHandling(t *testing.T) {
	var nilParser *sseParser
	if _, err := nilParser.Next(); err == nil || !strings.Contains(err.Error(), "reader is nil") {
		t.Fatalf("nil parser error = %v", err)
	}
	if _, err := (&sseParser{}).Next(); err == nil || !strings.Contains(err.Error(), "reader is nil") {
		t.Fatalf("nil reader error = %v", err)
	}

	parser := newSSEParser(strings.NewReader("event\ndata: tail"), DefaultMaxSSEEventBytes)
	event, err := parser.Next()
	if err != nil || string(event.Data) != "tail" {
		t.Fatalf("EOF-terminated event = %#v, error = %v", event, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := parser.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("post-EOF attempt %d error = %v", attempt+1, err)
		}
	}

	parser = newSSEParser(strings.NewReader("event"), DefaultMaxSSEEventBytes)
	if _, err := parser.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("bare field without data error = %v", err)
	}
}

func TestSSEParserEventByteCeiling(t *testing.T) {
	event := "data: 123\n\n"
	parser := newSSEParser(strings.NewReader(event), len(event))
	if _, err := parser.Next(); err != nil {
		t.Fatalf("boundary event failed: %v", err)
	}

	parser = newSSEParser(strings.NewReader(event), len(event)-1)
	if _, err := parser.Next(); !errors.Is(err, errSSEEventTooLarge) {
		t.Fatalf("oversized event error = %v", err)
	}
}

func TestSSEParserRejectsOversizedSingleLineWithoutScannerLimit(t *testing.T) {
	parser := newSSEParser(strings.NewReader("data: "+strings.Repeat("x", 9000)+"\n\n"), 128)
	if _, err := parser.Next(); !errors.Is(err, errSSEEventTooLarge) {
		t.Fatalf("oversized single-line error = %v", err)
	}
}

func TestSSEParserReassemblesDataLineLargerThanBufioBuffer(t *testing.T) {
	payload := strings.Repeat("x", 8192)
	encoded := "data: " + payload + "\n\n"
	parser := newSSEParser(strings.NewReader(encoded), len(encoded))
	event, err := parser.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(event.Data); got != payload {
		t.Fatalf("reassembled payload length = %d, want %d", len(got), len(payload))
	}
}

func TestSSEParserCountsOversizedCommentLines(t *testing.T) {
	parser := newSSEParser(strings.NewReader(":"+strings.Repeat("x", 8192)+"\n\n"), 128)
	if _, err := parser.Next(); !errors.Is(err, errSSEEventTooLarge) {
		t.Fatalf("oversized comment error = %v", err)
	}
}

func TestSSEParserCountsManySmallLinesAcrossWholeEvent(t *testing.T) {
	encoded := strings.Repeat(": keepalive\n", 32) + "data: ok\n\n"
	parser := newSSEParser(strings.NewReader(encoded), len(encoded)-1)
	if _, err := parser.Next(); !errors.Is(err, errSSEEventTooLarge) {
		t.Fatalf("aggregate event error = %v", err)
	}
}
