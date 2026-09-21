package obs

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// SafeAttr represents a strictly validated key-value attribute for structured logs.
//
// Invariant 9 prohibits logging handles, aliases, account IDs, endpoints, and IPs.
// SafeAttr constructors permit only safe types (integers, durations, booleans, closed enums),
// making it awkward to log arbitrary user input.
type SafeAttr struct {
	Key   string
	Value string
}

// Int returns a SafeAttr with an integer value.
func Int(k string, v int) SafeAttr {
	return SafeAttr{Key: k, Value: fmt.Sprintf("%d", v)}
}

// Int64 returns a SafeAttr with an int64 value.
func Int64(k string, v int64) SafeAttr {
	return SafeAttr{Key: k, Value: fmt.Sprintf("%d", v)}
}

// Duration returns a SafeAttr with a formatted duration.
func Duration(k string, d time.Duration) SafeAttr {
	return SafeAttr{Key: k, Value: d.String()}
}

// Bool returns a SafeAttr with a boolean value.
func Bool(k string, v bool) SafeAttr {
	return SafeAttr{Key: k, Value: fmt.Sprintf("%t", v)}
}

// Reason returns a SafeAttr for a closed DropReason enum.
func Reason(r DropReason) SafeAttr {
	return SafeAttr{Key: "reason", Value: string(r)}
}

// PushCode returns a SafeAttr for a closed PushFailureCode enum.
func PushCode(c PushFailureCode) SafeAttr {
	return SafeAttr{Key: "code", Value: string(c)}
}

// Logger provides safe structured logging without identifier leakage.
type Logger struct {
	out io.Writer
}

// NewLogger creates a Logger writing to out (defaulting to os.Stderr if nil).
func NewLogger(out io.Writer) *Logger {
	if out == nil {
		out = os.Stderr
	}
	return &Logger{out: out}
}

// Info logs an informational message with structured SafeAttr key-values.
func (l *Logger) Info(msg string, attrs ...SafeAttr) {
	l.log("INFO", msg, attrs...)
}

// Warn logs a warning message with structured SafeAttr key-values.
func (l *Logger) Warn(msg string, attrs ...SafeAttr) {
	l.log("WARN", msg, attrs...)
}

// Error logs an error message.
func (l *Logger) Error(msg string, err error) {
	if err != nil {
		_, _ = fmt.Fprintf(l.out, "[ERROR] %s: %v\n", msg, err)
	} else {
		_, _ = fmt.Fprintf(l.out, "[ERROR] %s\n", msg)
	}
}

func (l *Logger) log(level, msg string, attrs ...SafeAttr) {
	if len(attrs) == 0 {
		_, _ = fmt.Fprintf(l.out, "[%s] %s\n", level, msg)
		return
	}

	var sb strings.Builder
	for _, a := range attrs {
		sb.WriteString(" ")
		sb.WriteString(a.Key)
		sb.WriteString("=")
		sb.WriteString(a.Value)
	}
	_, _ = fmt.Fprintf(l.out, "[%s] %s%s\n", level, msg, sb.String())
}
