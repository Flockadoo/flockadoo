package relay

import "fmt"

// Outcome is how a request ended. Exactly one is recorded per request
// (invariant I5).
type Outcome int

const (
	Completed         Outcome = iota // the engine's answer reached the gateway in full
	RefusedBusy                      // no slot, or the gate is closed
	RefusedModel                     // a model this machine does not serve
	RefusedTooLarge                  // the body is over the limit
	RefusedBadRequest                // the body is not a JSON object
	RefusedUnverified                // model verification refused it
	EngineUnreachable                // the engine could not be asked
	EngineFailed                     // the engine's answer broke off
	ConsumerGone                     // the gateway went away
	DeadlineExceeded                 // the platform's time limit ran out
	Stalled                          // the engine sent nothing for the idle limit
	Panicked                         // a bug in the relay or the engine client
)

func (o Outcome) String() string {
	switch o {
	case Completed:
		return "completed"
	case RefusedBusy:
		return "refused_busy"
	case RefusedModel:
		return "refused_model"
	case RefusedTooLarge:
		return "refused_too_large"
	case RefusedBadRequest:
		return "refused_bad_request"
	case RefusedUnverified:
		return "refused_unverified"
	case EngineUnreachable:
		return "engine_unreachable"
	case EngineFailed:
		return "engine_failed"
	case ConsumerGone:
		return "consumer_gone"
	case DeadlineExceeded:
		return "deadline_exceeded"
	case Stalled:
		return "stalled"
	case Panicked:
		return "panicked"
	default:
		return fmt.Sprintf("outcome(%d)", int(o))
	}
}
