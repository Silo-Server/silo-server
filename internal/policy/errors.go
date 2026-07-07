package policy

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrPolicyEvalFailed marks failures that must be treated as fail-closed
	// policy evaluation outcomes.
	ErrPolicyEvalFailed = errors.New("policy eval failed")
	// ErrPolicyEvalTimeout marks evaluations that exceeded the eval budget. It
	// always accompanies ErrPolicyEvalFailed (fail-closed consumers keep
	// working unchanged) but lets operators and the activation guard tell a
	// slow policy apart from a broken one.
	ErrPolicyEvalTimeout = errors.New("policy eval timed out")
	// ErrPolicySlowEval marks a policy source rejected by the activation-time
	// cost guard: evaluating it exceeded the configured eval budget, so
	// activating it would convert to request-path failures for every viewer.
	ErrPolicySlowEval = errors.New("policy evaluation exceeds the eval budget")
	// ErrUnknownDecision marks requests for a decision that is not loaded.
	ErrUnknownDecision = errors.New("unknown policy decision")
	// ErrCompileFailed marks policy compilation failures with structured issues.
	ErrCompileFailed = errors.New("policy compile failed")
)

// CompileIssue identifies one policy compiler diagnostic.
type CompileIssue struct {
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	Message string `json:"message"`
}

// CompileError carries structured policy compiler diagnostics.
type CompileError struct {
	Issues []CompileIssue `json:"errors"`
}

func (e *CompileError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return ErrCompileFailed.Error()
	}
	messages := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		if issue.Row > 0 || issue.Col > 0 {
			messages = append(messages, fmt.Sprintf("%d:%d: %s", issue.Row, issue.Col, issue.Message))
			continue
		}
		messages = append(messages, issue.Message)
	}
	return fmt.Sprintf("%s: %s", ErrCompileFailed, strings.Join(messages, "; "))
}

func (e *CompileError) Is(target error) bool {
	return target == ErrCompileFailed
}
