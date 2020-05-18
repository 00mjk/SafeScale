/*
 * Copyright 2018-2020, CS Systemes d'Information, http://www.c-s.fr
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package concurrency

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	godebug "runtime/debug"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/CS-SI/SafeScale/lib/utils/debug"
	"github.com/CS-SI/SafeScale/lib/utils/strprocess"
	"github.com/CS-SI/SafeScale/lib/utils/temporal"
)

// Tracer ...
type Tracer struct {
	taskSig      string
	funcName     string
	inOutMessage string
	enabled      bool
	inDone       bool
	outDone      bool
	sw           temporal.Stopwatch
}

// IsLogActive ... FIXME
func IsLogActive(key string) bool {
	if logs := os.Getenv("SAFESCALE_OPTIONAL_LOGS"); logs != "" {
		return strings.Contains(logs, key)
	}
	return false
}

// NewTracer creates a new Tracer instance
func NewTracer(t Task, enabled bool, msg ...interface{}) *Tracer {
	tracer := Tracer{}
	if t != nil {
		tracer.taskSig, _ = t.GetSignature()
	}
	tracer.enabled = enabled

	message := strprocess.FormatStrings(msg...)
	if message == "" {
		message = "()"
	}

	// Build the message to trace
	if pc, file, line, ok := runtime.Caller(1); ok {
		if f := runtime.FuncForPC(pc); f != nil {
			tracer.funcName = f.Name()
			filename := strings.Replace(file, debug.SourceFilePartToRemove(), "", 1)
			tracer.inOutMessage = fmt.Sprintf("%s %s%s [%s:%d]", tracer.taskSig, filepath.Base(tracer.funcName), message, filename, line)
		}
	}

	return &tracer
}

// EnteringMessage returns the content of the message when entering the function
func (t *Tracer) EnteringMessage() string {
	return ">>>" + t.inOutMessage
}

// WithStopwatch will add a measure of duration between GoingIn and GoingOut.
// GoingOut will add the elapsed time in the log message (if it has to be logged...).
func (t *Tracer) WithStopwatch() *Tracer {
	if t.sw == nil {
		t.sw = temporal.NewStopwatch()
	}
	return t
}

// Entering logs the input message (signifying we are going in) using TRACE level
func (t *Tracer) Entering() *Tracer {
	if t.inDone {
		return t
	}
	if t.sw != nil {
		t.sw.Start()
	}
	if t.enabled {
		t.inDone = true
		logrus.Tracef(t.EnteringMessage())
	}
	return t
}

// OnExitTrace returns a function that will log the output message using TRACE level.
func (t *Tracer) OnExitTrace() {
	_ = t.Exiting()
}

// ExitingMessage returns the content of the message when exiting the function
func (t *Tracer) ExitingMessage() string {
	return "<<<" + t.inOutMessage
}

// Exiting logs the output message (signifying we are going out) using TRACE level and adds duration if WithStopwatch() has been called.
func (t *Tracer) Exiting() *Tracer {
	if t.outDone {
		return t
	}
	if t.sw != nil {
		t.sw.Stop()
	}
	if t.enabled {
		t.outDone = true
		msg := t.ExitingMessage()
		if t.sw != nil {
			msg += " (duration: " + t.sw.String() + ")"
		}
		logrus.Tracef(msg)
	}
	return t
}

// TraceMessage returns a string containing a trace message
func (t *Tracer) TraceMessage(msg ...interface{}) string {
	return "---" + t.inOutMessage + ": " + strprocess.FormatStrings(msg...)
}

// Trace traces a message
func (t *Tracer) Trace(msg ...interface{}) *Tracer {
	if t.enabled {
		logrus.Tracef(t.TraceMessage(msg...))
	}
	return t
}

// TraceCallStack logs the call stack as a trace (displayed only if tracing is enabled)
func (t *Tracer) TraceCallStack() *Tracer {
	return t.Trace("%s", string(godebug.Stack()))
}

// Stopwatch returns the stopwatch used (if a stopwatch has been asked with WithStopwatch() )
func (t *Tracer) Stopwatch() temporal.Stopwatch {
	return t.sw
}
