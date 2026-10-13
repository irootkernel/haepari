package managed

import (
	"errors"
	"fmt"
)

// These refinements retain existing public error codes and never include record
// values, observer text or references in operator messages.
type ManualCompletionRefusal struct{ Reason string }

func (e *ManualCompletionRefusal) Unwrap() error { return ErrBlocked }
func (e *ManualCompletionRefusal) Error() string {
	switch e.Reason {
	case "not_prepared":
		return "Manual preparation is not yet verified; inspect the original Job before completing it."
	case "retired":
		return "Manual completion authority has retired; inspect or wait for the original Job's settlement."
	case "deadline":
		return "The original manual deadline has expired; inspect or wait for the original Job's settlement."
	case "legacy":
		return "This manual session has no verified preparation record; inspect recovery for the original Job."
	default:
		return "The original Job is not awaiting a manual completion; inspect its current state."
	}
}

type ManualResultValidationError struct {
	Field, Reason string
	unsupported   bool
}

func (e *ManualResultValidationError) Unwrap() error {
	if e.unsupported {
		return ErrUnsupported
	}
	return ErrInvalid
}
func (e *ManualResultValidationError) Error() string {
	return fmt.Sprintf("Manual result %s %s.", e.Field, e.Reason)
}
func manualResultInvalid(field, reason string) error {
	return &ManualResultValidationError{Field: field, Reason: reason}
}

func manualDocumentError(err error) error {
	var field *closedFieldError
	if errors.As(err, &field) && safeManualField(field.Field) {
		return manualResultInvalid(field.Field, field.Reason)
	}
	return manualResultInvalid("document", "must use unique JSON keys and declared fields of the expected types")
}

// ValidateManualResultJSONKeys preserves the CLI's pre-transport refusal while
// naming a safe duplicate field and withholding all record values.
func ValidateManualResultJSONKeys(raw []byte) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return manualDocumentError(err)
	}
	return nil
}

// Field names are bounded ASCII identifiers; never echo arbitrary input text.
func safeManualField(field string) bool {
	if len(field) == 0 || len(field) > 64 {
		return false
	}
	for _, ch := range field {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			return false
		}
	}
	return true
}

type ManualScenarioDocumentError struct{}

func (e *ManualScenarioDocumentError) Unwrap() error { return ErrInvalid }
func (e *ManualScenarioDocumentError) Error() string {
	return "Manual Job manual.scenario_document must be included in input.selected_files."
}
