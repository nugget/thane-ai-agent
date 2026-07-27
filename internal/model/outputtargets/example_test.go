package outputtargets_test

import (
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/model/outputtargets"
)

// The primary usage pattern: resolve a declared target, validate the
// model's slot values against it, and publish the resulting payload.
func ExampleTarget_Normalize() {
	target, ok := outputtargets.Lookup("apple_watch.circular")
	if !ok {
		return
	}

	payload, err := target.Normalize(map[string]any{
		"value":       "64%",
		"fraction":    0.64,
		"gauge_color": "3fb950",
	})
	if err != nil {
		fmt.Println("rejected:", err)
		return
	}

	fmt.Println("state:", payload.State)
	fmt.Println("fraction:", payload.Attributes["fraction"])
	fmt.Println("gauge_color:", payload.Attributes["gauge_color"])
	// Output:
	// state: 64%
	// fraction: 0.64
	// gauge_color: #3FB950
}

// An over-budget value is rejected with an error a delegate can act on
// in one more call, rather than truncated into something unreadable.
func ExampleTarget_Normalize_overBudget() {
	target, _ := outputtargets.Lookup("apple_watch.circular")
	if _, err := target.Normalize(map[string]any{"value": "1013 hPa"}); err != nil {
		fmt.Println(err)
	}
	// Output:
	// slot "value": is 8 characters but this slot renders at most 6; shorten it rather than relying on truncation (got "1013 hPa")
}

func ExampleIDs() {
	for _, id := range outputtargets.IDs() {
		fmt.Println(id)
	}
	// Output:
	// apple_watch.circular
	// apple_watch.rectangular
}
