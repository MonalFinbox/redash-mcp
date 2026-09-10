package policy

import "fmt"

// Tier is the capability ceiling the process was started with. It is read
// once at startup and stored unexported, so no tool argument, no Redash
// response, and no environment change after boot can raise it.
type Tier int

const (
	// TierRead permits GET only. Redash serves cached results, so a server
	// at this tier cannot cause a single query to execute against a
	// warehouse.
	TierRead Tier = iota

	// TierExecute additionally permits re-running an already-saved query.
	// Never ad-hoc SQL, and never a mutation.
	TierExecute
)

func ParseTier(s string) (Tier, error) {
	switch s {
	case "", "read":
		return TierRead, nil
	case "execute":
		return TierExecute, nil
	default:
		return TierRead, fmt.Errorf("unknown tier %q (want \"read\" or \"execute\")", s)
	}
}

func (t Tier) String() string {
	if t == TierExecute {
		return "execute"
	}
	return "read"
}
