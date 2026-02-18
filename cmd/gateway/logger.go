package main

import (
	"fmt"
	"os"
)

// LogLevel defines the logging level
type LogLevel int

const (
	LogLevelError LogLevel = iota
	LogLevelWarn
	LogLevelInfo
	LogLevelDebug
)

var currentLogLevel LogLevel

func init() {
	// Set log level from environment variable
	level := os.Getenv("LOG_LEVEL")
	switch level {
	case "DEBUG":
		currentLogLevel = LogLevelDebug
	case "INFO":
		currentLogLevel = LogLevelInfo
	case "WARN":
		currentLogLevel = LogLevelWarn
	case "ERROR":
		currentLogLevel = LogLevelError
	default:
		currentLogLevel = LogLevelError // Production default: only errors
	}
}

func logError(format string, args ...interface{}) {
	if currentLogLevel >= LogLevelError {
		fmt.Printf("[Error] "+format+"\n", args...)
	}
}

func logWarn(format string, args ...interface{}) {
	if currentLogLevel >= LogLevelWarn {
		fmt.Printf("[Warn] "+format+"\n", args...)
	}
}

func logInfo(format string, args ...interface{}) {
	if currentLogLevel >= LogLevelInfo {
		fmt.Printf("[Info] "+format+"\n", args...)
	}
}

func logDebug(format string, args ...interface{}) {
	if currentLogLevel >= LogLevelDebug {
		fmt.Printf("[Debug] "+format+"\n", args...)
	}
}
