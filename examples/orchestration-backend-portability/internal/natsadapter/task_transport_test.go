package natsadapter

import (
	"testing"
	"time"
)

func TestWithAckWait(t *testing.T) {
	config := taskTransportConfig{ackWait: defaultAckWait}
	if err := WithAckWait(2 * time.Minute)(&config); err != nil {
		t.Fatal(err)
	}
	if config.ackWait != 2*time.Minute {
		t.Fatalf("ackWait = %s, want 2m", config.ackWait)
	}
}

func TestWithAckWaitRejectsNonPositiveValues(t *testing.T) {
	for _, wait := range []time.Duration{0, -time.Second} {
		config := taskTransportConfig{ackWait: defaultAckWait}
		if err := WithAckWait(wait)(&config); err == nil {
			t.Fatalf("WithAckWait(%s) succeeded, want error", wait)
		}
	}
}
