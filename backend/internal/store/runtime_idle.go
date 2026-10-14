package store

// A runtime keeps running with no viewer open for the inactivity period its
// owner chose, so its apps keep receiving messages and notifications; then
// it is stopped and its data saved. These are the periods offered.
var RuntimeIdleTimeouts = []int{
	30 * 60,
	60 * 60,
	3 * 60 * 60,
	6 * 60 * 60,
	8 * 60 * 60,
	12 * 60 * 60,
	24 * 60 * 60,
	3 * 24 * 60 * 60,
	7 * 24 * 60 * 60,
}

// DefaultRuntimeIdleTimeout is the shortest period, 30 minutes.
const DefaultRuntimeIdleTimeout = 30 * 60

func validRuntimeIdleTimeout(seconds int) bool {
	for _, allowed := range RuntimeIdleTimeouts {
		if seconds == allowed {
			return true
		}
	}
	return false
}
