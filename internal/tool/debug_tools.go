package tool

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrorAnalysis represents the analysis of an error
type ErrorAnalysis struct {
	ErrorType   string          `json:"error_type"`
	Location    *ErrorLocation  `json:"location,omitempty"`
	Message     string          `json:"message"`
	StackTrace  []StackFrame    `json:"stack_trace,omitempty"`
	Causes      []string        `json:"possible_causes"`
	Suggestions []FixSuggestion `json:"suggestions"`
	Severity    string          `json:"severity"`
	Language    string          `json:"language,omitempty"`
}

// ErrorLocation represents where an error occurred
type ErrorLocation struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
	Function string `json:"function,omitempty"`
}

// StackFrame represents a single frame in a stack trace
type StackFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
}

// FixSuggestion represents a suggested fix
type FixSuggestion struct {
	Description string `json:"description"`
	Code        string `json:"code,omitempty"`
	Line        int    `json:"line,omitempty"`
	Confidence  string `json:"confidence"` // high, medium, low
}

// AnalyzeErrorTool analyzes error messages and stack traces
type AnalyzeErrorTool struct {
	*BaseTool
}

// NewAnalyzeErrorTool creates a new error analysis tool
func NewAnalyzeErrorTool() *AnalyzeErrorTool {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"error_message": map[string]interface{}{
				"type":        "string",
				"description": "The error message or stack trace to analyze",
			},
			"language": map[string]interface{}{
				"type":        "string",
				"description": "Programming language (go, python, javascript, rust, etc.)",
			},
			"context": map[string]interface{}{
				"type":        "string",
				"description": "Additional context about when the error occurred",
			},
		},
		"required": []string{"error_message"},
	}

	return &AnalyzeErrorTool{
		BaseTool: NewBaseTool(
			"analyze_error",
			"Analyze error messages and stack traces to identify root causes and suggest fixes",
			schema,
		),
	}
}

// Execute runs the error analysis tool
func (t *AnalyzeErrorTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	errorMsg, ok := params["error_message"].(string)
	if !ok || errorMsg == "" {
		return nil, fmt.Errorf("error_message parameter is required")
	}

	language := ""
	if lang, ok := params["language"].(string); ok {
		language = lang
	}

	if language == "" {
		language = detectLanguageFromError(errorMsg)
	}

	analysis := analyzeError(errorMsg, language)
	return analysis, nil
}

func detectLanguageFromError(errorMsg string) string {
	// Go patterns
	if strings.Contains(errorMsg, "goroutine") ||
		strings.Contains(errorMsg, "panic:") ||
		regexp.MustCompile(`\.go:\d+`).MatchString(errorMsg) {
		return "go"
	}

	// Python patterns
	if strings.Contains(errorMsg, "Traceback (most recent call last)") ||
		strings.Contains(errorMsg, "File \"") && strings.Contains(errorMsg, "line") {
		return "python"
	}

	// JavaScript/Node.js patterns
	if strings.Contains(errorMsg, "at ") && strings.Contains(errorMsg, "(") ||
		strings.Contains(errorMsg, "TypeError:") ||
		strings.Contains(errorMsg, "ReferenceError:") {
		return "javascript"
	}

	// Rust patterns
	if strings.Contains(errorMsg, "thread 'main' panicked") ||
		strings.Contains(errorMsg, "error[Exxxx]:") {
		return "rust"
	}

	// Java patterns
	if strings.Contains(errorMsg, "Exception in thread") ||
		strings.Contains(errorMsg, "at ") && strings.Contains(errorMsg, "(") && strings.Contains(errorMsg, ".java:") {
		return "java"
	}

	return "unknown"
}

func analyzeError(errorMsg, language string) *ErrorAnalysis {
	analysis := &ErrorAnalysis{
		Message:     errorMsg,
		Language:    language,
		Severity:    "medium",
		Causes:      []string{},
		Suggestions: []FixSuggestion{},
	}

	switch language {
	case "go":
		analyzeGoError(analysis, errorMsg)
	case "python":
		analyzePythonError(analysis, errorMsg)
	case "javascript":
		analyzeJavaScriptError(analysis, errorMsg)
	case "rust":
		analyzeRustError(analysis, errorMsg)
	case "java":
		analyzeJavaError(analysis, errorMsg)
	default:
		analyzeGenericError(analysis, errorMsg)
	}

	return analysis
}

func analyzeGoError(analysis *ErrorAnalysis, errorMsg string) {
	analysis.ErrorType = "Go Error"

	// Parse panic
	if strings.Contains(errorMsg, "panic:") {
		analysis.ErrorType = "Panic"
		analysis.Severity = "critical"

		// Extract panic message
		panicRegex := regexp.MustCompile(`panic: (.+)`)
		if matches := panicRegex.FindStringSubmatch(errorMsg); len(matches) > 1 {
			panicMsg := matches[1]
			analysis.Message = panicMsg

			if strings.Contains(panicMsg, "nil pointer dereference") {
				analysis.Causes = append(analysis.Causes,
					"Accessing a nil pointer",
					"Using an uninitialized pointer variable",
				)
				analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
					Description: "Add nil check before accessing the pointer",
					Code:        "if ptr != nil {\n    // access ptr\n}",
					Confidence:  "high",
				})
			} else if strings.Contains(panicMsg, "index out of range") {
				analysis.Causes = append(analysis.Causes,
					"Array/slice index exceeds bounds",
					"Empty slice accessed with index",
				)
				analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
					Description: "Check slice length before indexing",
					Code:        "if idx < len(slice) {\n    value := slice[idx]\n}",
					Confidence:  "high",
				})
			}
		}
	}

	// Parse stack trace
	analysis.StackTrace = parseGoStackTrace(errorMsg)
	if len(analysis.StackTrace) > 0 {
		analysis.Location = &ErrorLocation{
			File:     analysis.StackTrace[0].File,
			Line:     analysis.StackTrace[0].Line,
			Function: analysis.StackTrace[0].Function,
		}
	}

	// Common Go errors
	if strings.Contains(errorMsg, "undefined:") {
		analysis.ErrorType = "Undefined Symbol"
		analysis.Causes = append(analysis.Causes,
			"Variable or function not declared",
			"Missing import",
			"Typo in identifier name",
		)
	}

	if strings.Contains(errorMsg, "cannot use") && strings.Contains(errorMsg, "as type") {
		analysis.ErrorType = "Type Mismatch"
		analysis.Causes = append(analysis.Causes,
			"Incorrect type used where another expected",
			"Missing type conversion",
			"Interface implementation mismatch",
		)
	}
}

func parseGoStackTrace(errorMsg string) []StackFrame {
	var frames []StackFrame
	lines := strings.Split(errorMsg, "\n")

	// Pattern: function_name(0x0, 0x0)
	//    /path/to/file.go:42 +0x25
	funcRegex := regexp.MustCompile(`^(\S+)\(`)
	fileRegex := regexp.MustCompile(`^\s+(\S+):(\d+)`)

	for i := 0; i < len(lines); i++ {
		if matches := funcRegex.FindStringSubmatch(lines[i]); len(matches) > 1 {
			function := matches[1]
			if i+1 < len(lines) {
				if fileMatches := fileRegex.FindStringSubmatch(lines[i+1]); len(fileMatches) > 2 {
					file := fileMatches[1]
					line, _ := strconv.Atoi(fileMatches[2])
					frames = append(frames, StackFrame{
						Function: function,
						File:     file,
						Line:     line,
					})
					i++ // Skip next line
				}
			}
		}
	}

	return frames
}

func analyzePythonError(analysis *ErrorAnalysis, errorMsg string) {
	analysis.ErrorType = "Python Exception"

	// Parse exception type
	exceptionRegex := regexp.MustCompile(`(\w+Error):\s*(.+)`)
	if matches := exceptionRegex.FindStringSubmatch(errorMsg); len(matches) > 2 {
		analysis.ErrorType = matches[1]
		analysis.Message = matches[2]
	}

	// Parse traceback
	analysis.StackTrace = parsePythonTraceback(errorMsg)
	if len(analysis.StackTrace) > 0 {
		analysis.Location = &ErrorLocation{
			File:     analysis.StackTrace[0].File,
			Line:     analysis.StackTrace[0].Line,
			Function: analysis.StackTrace[0].Function,
		}
	}

	// Specific error analysis
	switch analysis.ErrorType {
	case "KeyError":
		analysis.Causes = append(analysis.Causes,
			"Accessing a non-existent dictionary key",
			"Key was deleted or never added",
		)
		analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
			Description: "Use dict.get() or check key existence",
			Code:        "value = my_dict.get('key', default_value)\n# or\nif 'key' in my_dict:\n    value = my_dict['key']",
			Confidence:  "high",
		})
	case "IndexError":
		analysis.Causes = append(analysis.Causes,
			"List index out of range",
			"Accessing empty list",
		)
		analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
			Description: "Check list length before indexing",
			Code:        "if idx < len(my_list):\n    value = my_list[idx]",
			Confidence:  "high",
		})
	case "TypeError":
		if strings.Contains(errorMsg, "NoneType") {
			analysis.Causes = append(analysis.Causes,
				"Calling method on None",
				"Function returned None unexpectedly",
			)
			analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
				Description: "Add None check before method call",
				Code:        "if obj is not None:\n    obj.method()",
				Confidence:  "high",
			})
		} else {
			analysis.Causes = append(analysis.Causes,
				"Wrong type used for operation",
				"Missing type conversion",
			)
		}
	case "ZeroDivisionError":
		analysis.Causes = append(analysis.Causes,
			"Division by zero",
			"Modulo by zero",
		)
		analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
			Description: "Check divisor before division",
			Code:        "if divisor != 0:\n    result = dividend / divisor\nelse:\n    result = 0  # or handle appropriately",
			Confidence:  "high",
		})
	}
}

func parsePythonTraceback(errorMsg string) []StackFrame {
	var frames []StackFrame
	lines := strings.Split(errorMsg, "\n")

	// Pattern: File "/path/to/file.py", line 42, in function_name
	traceRegex := regexp.MustCompile(`File "([^"]+)", line (\d+), in (\S+)`)

	for _, line := range lines {
		if matches := traceRegex.FindStringSubmatch(line); len(matches) > 3 {
			lineNum, _ := strconv.Atoi(matches[2])
			frames = append(frames, StackFrame{
				File:     matches[1],
				Line:     lineNum,
				Function: matches[3],
			})
		}
	}

	// Reverse to get most recent first
	for i, j := 0, len(frames)-1; i < j; i, j = i+1, j-1 {
		frames[i], frames[j] = frames[j], frames[i]
	}

	return frames
}

func analyzeJavaScriptError(analysis *ErrorAnalysis, errorMsg string) {
	analysis.ErrorType = "JavaScript Error"

	// Parse error type
	errorRegex := regexp.MustCompile(`^(\w+Error):\s*(.+)`)
	if matches := errorRegex.FindStringSubmatch(errorMsg); len(matches) > 2 {
		analysis.ErrorType = matches[1]
		analysis.Message = matches[2]
	}

	// Parse stack trace
	analysis.StackTrace = parseJavaScriptStackTrace(errorMsg)
	if len(analysis.StackTrace) > 0 {
		analysis.Location = &ErrorLocation{
			File:     analysis.StackTrace[0].File,
			Line:     analysis.StackTrace[0].Line,
			Function: analysis.StackTrace[0].Function,
		}
	}

	// Specific error analysis
	switch analysis.ErrorType {
	case "TypeError":
		if strings.Contains(errorMsg, "Cannot read property") ||
			strings.Contains(errorMsg, "Cannot read properties") {
			analysis.Causes = append(analysis.Causes,
				"Accessing property on undefined/null",
				"Object not initialized before use",
			)
			analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
				Description: "Use optional chaining or null check",
				Code:        "const value = obj?.property;\n// or\nif (obj) {\n    const value = obj.property;\n}",
				Confidence:  "high",
			})
		} else if strings.Contains(errorMsg, "is not a function") {
			analysis.Causes = append(analysis.Causes,
				"Calling non-function as function",
				"Variable reassigned to non-function",
			)
		}
	case "ReferenceError":
		analysis.Causes = append(analysis.Causes,
			"Variable not declared",
			"Variable used before definition",
			"Typo in variable name",
		)
	case "SyntaxError":
		analysis.Causes = append(analysis.Causes,
			"Invalid JavaScript syntax",
			"Missing bracket or parenthesis",
		)
		analysis.Severity = "high"
	}
}

func parseJavaScriptStackTrace(errorMsg string) []StackFrame {
	var frames []StackFrame
	lines := strings.Split(errorMsg, "\n")

	// Pattern: at functionName (file.js:42:10)
	// or: at file.js:42:10
	traceRegex := regexp.MustCompile(`at\s+(?:(\S+)\s+\()?([^:]+):(\d+):(\d+)\)?`)

	for _, line := range lines {
		if matches := traceRegex.FindStringSubmatch(line); len(matches) > 4 {
			function := matches[1]
			if function == "" {
				function = "<anonymous>"
			}
			lineNum, _ := strconv.Atoi(matches[3])
			frames = append(frames, StackFrame{
				Function: function,
				File:     matches[2],
				Line:     lineNum,
			})
		}
	}

	return frames
}

func analyzeRustError(analysis *ErrorAnalysis, errorMsg string) {
	analysis.ErrorType = "Rust Error"

	if strings.Contains(errorMsg, "panicked") {
		analysis.ErrorType = "Panic"
		analysis.Severity = "critical"
	}

	// Parse error location
	locationRegex := regexp.MustCompile(`-->\s+(\S+):(\d+):(\d+)`)
	if matches := locationRegex.FindStringSubmatch(errorMsg); len(matches) > 3 {
		line, _ := strconv.Atoi(matches[2])
		col, _ := strconv.Atoi(matches[3])
		analysis.Location = &ErrorLocation{
			File:   matches[1],
			Line:   line,
			Column: col,
		}
	}

	// Common Rust errors
	if strings.Contains(errorMsg, "borrow of moved value") {
		analysis.Causes = append(analysis.Causes,
			"Value was moved and can no longer be used",
			"Missing Clone trait implementation",
		)
		analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
			Description: "Clone the value or use references",
			Code:        "let new_val = val.clone();\n// or\nfn func(val: &Type) { }",
			Confidence:  "high",
		})
	}

	if strings.Contains(errorMsg, "cannot borrow") && strings.Contains(errorMsg, "as mutable") {
		analysis.Causes = append(analysis.Causes,
			"Multiple mutable borrows",
			"Borrow checker violation",
		)
	}
}

func analyzeJavaError(analysis *ErrorAnalysis, errorMsg string) {
	analysis.ErrorType = "Java Exception"

	// Parse exception type
	exceptionRegex := regexp.MustCompile(`(\w+Exception):\s*(.+)`)
	if matches := exceptionRegex.FindStringSubmatch(errorMsg); len(matches) > 2 {
		analysis.ErrorType = matches[1]
		analysis.Message = matches[2]
	}

	// Parse stack trace
	analysis.StackTrace = parseJavaStackTrace(errorMsg)
	if len(analysis.StackTrace) > 0 {
		analysis.Location = &ErrorLocation{
			File:     analysis.StackTrace[0].File,
			Line:     analysis.StackTrace[0].Line,
			Function: analysis.StackTrace[0].Function,
		}
	}

	if strings.Contains(analysis.ErrorType, "NullPointerException") {
		analysis.Causes = append(analysis.Causes,
			"Method called on null object",
			"Object not initialized",
		)
		analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
			Description: "Add null check before method call",
			Code:        "if (obj != null) {\n    obj.method();\n}",
			Confidence:  "high",
		})
	}
}

func parseJavaStackTrace(errorMsg string) []StackFrame {
	var frames []StackFrame
	lines := strings.Split(errorMsg, "\n")

	// Pattern: at package.Class.method(File.java:42)
	traceRegex := regexp.MustCompile(`at\s+([\w.]+)\.([\w<>]+)\(([^)]+)\)`)

	for _, line := range lines {
		if matches := traceRegex.FindStringSubmatch(line); len(matches) > 3 {
			function := matches[1] + "." + matches[2]
			location := matches[3]

			// Parse file:line
			fileLineRegex := regexp.MustCompile(`(\w+\.\w+):(\d+)`)
			if flMatches := fileLineRegex.FindStringSubmatch(location); len(flMatches) > 2 {
				lineNum, _ := strconv.Atoi(flMatches[2])
				frames = append(frames, StackFrame{
					Function: function,
					File:     flMatches[1],
					Line:     lineNum,
				})
			}
		}
	}

	return frames
}

func analyzeGenericError(analysis *ErrorAnalysis, errorMsg string) {
	analysis.ErrorType = "Unknown Error"
	analysis.Causes = append(analysis.Causes,
		"Unable to determine specific cause",
		"Review error message for details",
	)
	analysis.Suggestions = append(analysis.Suggestions, FixSuggestion{
		Description: "Check logs and reproduce the issue",
		Confidence:  "low",
	})
}
