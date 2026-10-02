package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInvestigationLeasePrivateIdentity(t *testing.T) {
	for _, value := range []any{
		InvestigationLease{AgentName: "agent", EntityID: "document", AcquisitionID: "private-token"},
		InvestigationLeaseRequest{AgentName: "agent", EntityID: "document", AcquisitionID: "private-token"},
	} {
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "AcquisitionID") {
			t.Fatalf("private ownership identity serialized: %s, %v", encoded, err)
		}
	}
}
