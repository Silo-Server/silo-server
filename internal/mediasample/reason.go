package mediasample

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Reason classifies why sampling failed. Each failed attempt records how it
// ended (canceled, timeout, start, exit, args, empty, output, unsupported,
// capabilities); Classify reads a failed run and names its cause (canceled,
// timeout, invalid_data, killed, unsupported, capabilities, no_stream,
// failed), which tells a caller whether trying again can help.
type Reason string

// Attempt reasons.
const (
	// ReasonCanceled means the caller's context ended.
	ReasonCanceled Reason = "canceled"
	// ReasonTimeout means the attempt's own timeout passed.
	ReasonTimeout Reason = "timeout"
	// ReasonStart means ffmpeg could not be started.
	ReasonStart Reason = "start"
	// ReasonExit means ffmpeg exited unsuccessfully.
	ReasonExit Reason = "exit"
	// ReasonArgs means the attempt could not be turned into arguments.
	ReasonArgs Reason = "args"
	// ReasonEmpty means ffmpeg succeeded without writing the image asked for,
	// as when the frame time lies past the end of the video, or without
	// decoding enough of the samples a sheet asked for.
	ReasonEmpty Reason = "empty"
	// ReasonOutput means ffmpeg's output did not match its log, so its
	// frames could not be placed.
	ReasonOutput Reason = "output"
)

// Run causes, from Classify. ReasonCanceled and ReasonTimeout are causes too.
const (
	// ReasonInvalidData means the input could not be demuxed or decoded.
	ReasonInvalidData Reason = "invalid_data"
	// ReasonKilled means ffmpeg was killed by a signal it did not ask for,
	// such as the out-of-memory killer's.
	ReasonKilled Reason = "killed"
	// ReasonUnsupported means this ffmpeg or host lacks something the request
	// needs, such as a filter, an option, a decoder, or a render device, or
	// that the request forbids what the attempt would need (software tone
	// mapping). An attempt refused before ffmpeg starts records it as its own
	// reason.
	ReasonUnsupported Reason = "unsupported"
	// ReasonCapabilities means the capability listing a request needs (see
	// LoadCapabilities) could not be run. It is also an attempt reason.
	ReasonCapabilities Reason = "capabilities"
	// ReasonNoStream means the input has no stream an output needs.
	ReasonNoStream Reason = "no_stream"
	// ReasonFailed is any other failure.
	ReasonFailed Reason = "failed"
)

// AttemptError is one failed attempt.
type AttemptError struct {
	Decoder string
	Reason  Reason
	Err     error
	// StderrTail is the end of ffmpeg's log, bounded in size.
	StderrTail string
}

// Error reports a run in which every attempt failed.
type Error struct {
	// Reason is the last attempt's reason.
	Reason   Reason
	Attempts []AttemptError
}

func (e *Error) Error() string {
	if len(e.Attempts) == 0 {
		return fmt.Sprintf("ffmpeg sampling failed (%s)", e.Reason)
	}
	last := e.Attempts[len(e.Attempts)-1]
	var b strings.Builder
	fmt.Fprintf(&b, "ffmpeg sampling failed (%s)", e.Reason)
	if len(e.Attempts) > 1 {
		fmt.Fprintf(&b, " after %d attempts", len(e.Attempts))
	}
	if last.Err != nil {
		fmt.Fprintf(&b, ": %v", last.Err)
	}
	if line := lastLine(last.StderrTail); line != "" {
		fmt.Fprintf(&b, ": %s", line)
	}
	return b.String()
}

// Unwrap exposes every attempt's error, so errors.Is sees a context error.
func (e *Error) Unwrap() []error {
	errs := make([]error, 0, len(e.Attempts))
	for _, attempt := range e.Attempts {
		if attempt.Err != nil {
			errs = append(errs, attempt.Err)
		}
	}
	return errs
}

// Permanent reports whether the cause belongs to the input itself, so the
// same request on the same file fails again whatever ffmpeg or server runs
// it. Other causes may pass: a timeout on a busy server, a killed process, or
// an ffmpeg that is later upgraded.
func (r Reason) Permanent() bool {
	return r == ReasonInvalidData || r == ReasonNoStream
}

// Log messages that name a cause. ffmpeg's wording differs between versions
// and demuxers, so each cause lists the forms seen in production builds.
var (
	invalidDataMessages = []string{
		"invalid data found when processing input",
		"invalid nal unit size",
		"error splitting the input into nal units",
		"invalid as first byte of an ebml number",
		"moov atom not found",
	}
	noStreamMessages = []string{
		"matches no streams",
		"does not contain any stream",
	}
	unsupportedMessages = []string{
		"no such filter",
		"unrecognized option",
		"option not found",
		"unknown encoder",
		"unknown decoder",
		"decoder not found",
		"requested output format",
	}
)

// Classify names the cause of a failed run. It reads the last attempt of an
// *Error, whose reason and log tail describe how the run finally ended; any
// other error, apart from a context error, is ReasonFailed. A nil error has
// no cause.
func Classify(err error) Reason {
	if err == nil {
		return ""
	}
	var runErr *Error
	if !errors.As(err, &runErr) || len(runErr.Attempts) == 0 {
		switch {
		case errors.Is(err, context.Canceled):
			return ReasonCanceled
		case errors.Is(err, context.DeadlineExceeded):
			return ReasonTimeout
		}
		return ReasonFailed
	}
	return runErr.Attempts[len(runErr.Attempts)-1].Cause()
}

// Cause names the cause of one failed attempt, as Classify does for a run's
// last attempt.
func (a AttemptError) Cause() Reason {
	switch a.Reason {
	case ReasonCanceled, ReasonTimeout, ReasonUnsupported, ReasonCapabilities:
		return a.Reason
	case ReasonExit:
	default:
		return ReasonFailed
	}
	// A process killed from outside may have logged recoverable decode
	// errors first, so the kill decides.
	if a.Err != nil && strings.Contains(a.Err.Error(), "signal: killed") {
		return ReasonKilled
	}
	log := strings.ToLower(a.StderrTail)
	switch {
	case containsAny(log, noStreamMessages):
		return ReasonNoStream
	case containsAny(log, unsupportedMessages):
		return ReasonUnsupported
	case containsAny(log, invalidDataMessages):
		return ReasonInvalidData
	}
	return ReasonFailed
}

func containsAny(s string, substrings []string) bool {
	for _, substring := range substrings {
		if strings.Contains(s, substring) {
			return true
		}
	}
	return false
}
