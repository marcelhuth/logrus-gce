package logrusgce

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

type severity string

const (
	logrusToCallerSkip = 5
)

const (
	severityDEBUG     severity = "DEBUG"
	severityINFO      severity = "INFO"
	severityNOTICE    severity = "NOTICE"
	severityWARNING   severity = "WARNING"
	severityERROR     severity = "ERROR"
	severityCRITICAL  severity = "CRITICAL"
	severityALERT     severity = "ALERT"
	severityEMERGENCY severity = "EMERGENCY"
)

var (
	levelsLogrusToGCE = map[logrus.Level]severity{
		logrus.DebugLevel: severityDEBUG,
		logrus.InfoLevel:  severityINFO,
		logrus.WarnLevel:  severityWARNING,
		logrus.ErrorLevel: severityERROR,
		logrus.FatalLevel: severityCRITICAL,
		logrus.PanicLevel: severityALERT,
	}
)

var (
	stackSkipsCallers = make([]uintptr, 0, 20)
	stackSkips        = map[logrus.Level]int{}
	stackSkipsMu      = sync.RWMutex{}
)

var (
	ErrSkipNotFound = errors.New("could not find skips for log level")
)

type sourceLocation struct {
	File string `json:"file"`
	Line int    `json:"line"`
	// FunctionName is "function" in JSON: https://cloud.google.com/logging/docs/reference/v2/rest/v2/LogEntry?hl=de#logentrysourcelocation
	FunctionName string `json:"function"`
}

func getSkipLevel(level logrus.Level) (int, error) {
	stackSkipsMu.RLock()
	if skip, ok := stackSkips[level]; ok {
		defer stackSkipsMu.RUnlock()
		return skip, nil
	}
	stackSkipsMu.RUnlock()

	stackSkipsMu.Lock()
	defer stackSkipsMu.Unlock()
	if skip, ok := stackSkips[level]; ok {
		return skip, nil
	}

	// detect until we escape logrus back to the client package
	// skip out of runtime and logrusgce package, hence 3
	stackSkipsCallers := make([]uintptr, 20)
	runtime.Callers(3, stackSkipsCallers)
	for i, pc := range stackSkipsCallers {
		f := runtime.FuncForPC(pc)
		if strings.HasPrefix(f.Name(), "github.com/sirupsen/logrus") {
			continue
		}
		stackSkips[level] = i + 1
		return i + 1, nil
	}
	return 0, ErrSkipNotFound
}

type GCEFormatter struct {
	withSourceInfo bool
}

func NewGCEFormatter(withSourceInfo bool) *GCEFormatter {
	return &GCEFormatter{withSourceInfo: withSourceInfo}
}

func (f *GCEFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	data := make(logrus.Fields, len(entry.Data)+3)
	for k, v := range entry.Data {
		switch v := v.(type) {
		case error:
			// Otherwise errors are ignored by `encoding/json`
			// https://github.com/sirupsen/logrus/issues/137
			data[k] = v.Error()
		default:
			data[k] = v
		}
	}

	data["time"] = entry.Time.Format(time.RFC3339Nano)
	data["severity"] = levelsLogrusToGCE[entry.Level]
	data["message"] = entry.Message

	if f.withSourceInfo {
		skip, err := getSkipLevel(entry.Level)
		if err != nil {
			return nil, err
		}
		if pc, file, line, ok := runtime.Caller(skip); ok {
			f := runtime.FuncForPC(pc)
			data["logging.googleapis.com/sourceLocation"] = map[string]interface{}{
				"file": file,
				"line": line,
				// FunctionName is "function" in JSON: https://cloud.google.com/logging/docs/reference/v2/rest/v2/LogEntry?hl=de#logentrysourcelocation
				"function": f.Name(),
			}
		}
	}

	serialized, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal fields to JSON, %v", err)
	}
	return append(serialized, '\n'), nil
}
