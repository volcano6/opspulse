package logger

import "testing"

func TestSetup_Info(_ *testing.T) {
	// Should not panic
	Setup(false)
}

func TestSetup_Debug(_ *testing.T) {
	// Should not panic
	Setup(true)
}
