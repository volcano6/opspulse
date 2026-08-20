package logger

import "testing"

func TestSetup_Info(t *testing.T) {
	// Should not panic
	Setup(false)
}

func TestSetup_Debug(t *testing.T) {
	// Should not panic
	Setup(true)
}
