package telemetry

import "fmt"

// TestThresholdCalculations demonstrates threshold calculations for circuit breaker
// This is kept as a reference but disabled to avoid build issues
func testThresholdCalculationsDisabled() {
	// Test threshold calculations
	testCases := []int{2, 3, 4, 5, 10, 20}

	for _, maxFailures := range testCases {
		// Calculate halfway point using the fixed logic
		halfwayPoint := (maxFailures + 1) / 2
		percentage := (halfwayPoint * 100) / maxFailures

		fmt.Printf("MaxFailures=%d: halfway=%d (%.0f%%)\n",
			maxFailures, halfwayPoint, float64(percentage))

		// Show when warnings would trigger
		fmt.Printf("  - First failure: 1\n")
		if maxFailures > 2 {
			fmt.Printf("  - Halfway warning: %d\n", halfwayPoint)
			if maxFailures > 3 {
				fmt.Printf("  - One before open: %d\n", maxFailures-1)
			}
		}
		fmt.Printf("  - Circuit opens: %d\n\n", maxFailures)
	}
}
