package agent

// RCAResult is the structured root-cause analysis output produced by the LLM.
// CON-003: sonic tags — no encoding/json.
type RCAResult struct {
	Summary             string   `json:"summary"`
	RootCause           string   `json:"rootCause"`
	ContributingFactors []string `json:"contributingFactors,omitempty"`
	RemediationSteps    []string `json:"remediationSteps,omitempty"`
	Confidence          float64  `json:"confidence"`
	Reasoning           string   `json:"reasoning,omitempty"`
}

// Message is a single turn in an LLM conversation.
type Message struct {
	Role       string     `json:"role"`              // system | user | assistant | tool
	Content    string     `json:"content,omitempty"` // omitted when tool_calls present
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // set on tool-result messages
	Name       string     `json:"name,omitempty"`         // tool name on tool-result messages
}

// ToolCall is an LLM-requested function invocation.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the function name and its serialized arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

// Request is sent to the LLM provider.
type Request struct {
	Model     string           `json:"model"`
	Messages  []Message        `json:"messages"`
	Tools     []ToolDefinition `json:"tools,omitempty"`
	MaxTokens int              `json:"max_tokens,omitempty"`
	Stream    bool             `json:"stream,omitempty"` // set by CompleteStream; false omitted so Complete is unaffected
}

// Response is received from the LLM provider.
type Response struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice is one completion candidate.
type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage tracks token consumption.
type Usage struct {
	// ponytail: only total_tokens — prompt/completion split not needed for MVP audit
	TotalTokens int `json:"total_tokens"`
}

// ToolDefinition describes a callable tool exposed to the LLM.
type ToolDefinition struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef is the schema of a tool the LLM may call.
type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}
