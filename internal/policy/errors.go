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
