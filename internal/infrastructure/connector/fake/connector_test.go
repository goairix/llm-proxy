package fake

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

var fakeNow = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

func TestFakeCompleteProducesDeterministicTextAndUsage(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	connector := New(Options{IDGenerator: fixedIDs(id), Clock: func() time.Time { return fakeNow }})

	response, err := connector.Complete(context.Background(), validInvocation())
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != id || response.Model != "assistant" || !response.CreatedAt.Equal(fakeNow) || response.StopReason != inference.StopEndTurn {
		t.Fatalf("response=%+v", response)
	}
	if len(response.Content) != 1 || response.Content[0].Text == nil || response.Content[0].Text.Text != "回显: hello" {
		t.Fatalf("content=%+v", response.Content)
	}
	if response.Usage.InputTokens <= 0 || response.Usage.OutputTokens <= 0 {
		t.Fatalf("usage=%+v", response.Usage)
	}

	again := New(Options{IDGenerator: fixedIDs(id), Clock: func() time.Time { return fakeNow }})
	againResponse, err := again.Complete(context.Background(), validInvocation())
	if err != nil || againResponse.Usage != response.Usage || againResponse.Content[0].Text.Text != response.Content[0].Text.Text {
		t.Fatalf("deterministic response=%+v err=%v", againResponse, err)
	}
}

func TestFakeCompleteHandlesImageToolResultAndZeroOutput(t *testing.T) {
	tests := []struct {
		name    string
		request func() inference.Request
		want    string
		stop    inference.StopReason
		empty   bool
	}{
		{name: "image", request: func() inference.Request {
			request := validRequest()
			request.Messages[0].Content = []inference.ContentBlock{{Type: inference.ContentImage, Image: &inference.ImageContent{
				Source: inference.ImageSource{Type: inference.ImageBase64, MediaType: "image/png", Data: "aW1hZ2U="},
			}}}
			return request
		}, want: "回显: [image:image/png]", stop: inference.StopEndTurn},
		{name: "tool result", request: toolResultRequest, want: "工具结果已接收: {\"temperature\":30}", stop: inference.StopEndTurn},
		{name: "max tokens zero", request: func() inference.Request {
			request := validRequest()
			request.MaxTokens = inference.Some(int64(0))
			return request
		}, stop: inference.StopMaxTokens, empty: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connector := New(Options{IDGenerator: fixedIDs(uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow }})
			invocation := validInvocation()
			invocation.Request = test.request()
			response, err := connector.Complete(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			if response.StopReason != test.stop {
				t.Fatalf("stop=%s", response.StopReason)
			}
			if test.empty {
				if len(response.Content) != 0 || response.Usage.OutputTokens != 0 {
					t.Fatalf("zero output response=%+v", response)
				}
			} else if len(response.Content) != 1 || response.Content[0].Text == nil || response.Content[0].Text.Text != test.want {
				t.Fatalf("content=%+v", response.Content)
			}
		})
	}
}

func TestFakeCompleteDoesNotReuseToolResultFromAnOlderTurn(t *testing.T) {
	connector := New(Options{IDGenerator: fixedIDs(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow }})
	invocation := validInvocation()
	invocation.Request = toolResultRequest()
	invocation.Request.Messages = append(invocation.Request.Messages,
		inference.Message{Role: inference.RoleAssistant, Content: []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: "30 degrees"}}}},
		inference.Message{Role: inference.RoleUser, Content: []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: "thanks"}}}},
	)

	response, err := connector.Complete(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content[0].Text.Text != "回显: thanks" {
		t.Fatalf("content=%q", response.Content[0].Text.Text)
	}
}

func TestFakeCompleteProducesToolCallFromSchema(t *testing.T) {
	responseID := uuid.Must(uuid.NewV7())
	callID := uuid.Must(uuid.NewV7())
	connector := New(Options{IDGenerator: fixedIDs(responseID, callID), Clock: func() time.Time { return fakeNow }})
	invocation := validInvocation()
	invocation.Request.Tools = []inference.Tool{{
		Name: "weather", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
	}}

	response, err := connector.Complete(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != inference.StopToolUse || len(response.Content) != 1 || response.Content[0].ToolCall == nil {
		t.Fatalf("response=%+v", response)
	}
	call := response.Content[0].ToolCall
	if call.ID != "call_"+callID.String() || call.Name != "weather" || string(call.Arguments) != `{"city":"fake"}` {
		t.Fatalf("tool call=%+v", call)
	}
}

func TestFakeCompleteHonorsToolChoice(t *testing.T) {
	tools := []inference.Tool{
		{Name: "first", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "second", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	tests := []struct {
		name       string
		choice     *inference.ToolChoice
		wantTool   string
		wantReason inference.StopReason
	}{
		{name: "none", choice: &inference.ToolChoice{Mode: inference.ToolChoiceNone}, wantReason: inference.StopEndTurn},
		{name: "required", choice: &inference.ToolChoice{Mode: inference.ToolChoiceRequired}, wantTool: "first", wantReason: inference.StopToolUse},
		{name: "specific", choice: &inference.ToolChoice{Mode: inference.ToolChoiceSpecific, Name: "second"}, wantTool: "second", wantReason: inference.StopToolUse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connector := New(Options{IDGenerator: fixedIDs(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow }})
			invocation := validInvocation()
			invocation.Request.Tools = tools
			invocation.Request.ToolChoice = test.choice
			response, err := connector.Complete(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			if response.StopReason != test.wantReason {
				t.Fatalf("stop reason=%q", response.StopReason)
			}
			if test.wantTool == "" {
				if len(response.Content) != 1 || response.Content[0].Text == nil {
					t.Fatalf("content=%+v", response.Content)
				}
				return
			}
			if len(response.Content) != 1 || response.Content[0].ToolCall == nil || response.Content[0].ToolCall.Name != test.wantTool {
				t.Fatalf("content=%+v", response.Content)
			}
		})
	}
}

func TestFakeCompleteProducesStructuredFixture(t *testing.T) {
	connector := New(Options{IDGenerator: fixedIDs(uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow }})
	invocation := validInvocation()
	invocation.Request.StructuredOutput = &inference.StructuredOutput{
		Type:   inference.StructuredJSONSchema,
		Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"},"count":{"type":"integer"}},"required":["answer","count"]}`),
	}

	response, err := connector.Complete(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if len(response.Content) != 1 || response.Content[0].Text == nil || json.Unmarshal([]byte(response.Content[0].Text.Text), &fixture) != nil {
		t.Fatalf("structured content=%+v", response.Content)
	}
	if fixture["answer"] != "fake" || fixture["count"] != float64(1) {
		t.Fatalf("fixture=%+v", fixture)
	}
}

func TestFixtureValueHonorsSupportedJSONSchemaConstraints(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		assert func(*testing.T, any)
	}{
		{
			name:   "integer bounds and multiple",
			schema: `{"type":"integer","minimum":10,"maximum":20,"multipleOf":5}`,
			assert: func(t *testing.T, value any) {
				if value != int64(10) {
					t.Fatalf("value=%#v", value)
				}
			},
		},
		{
			name:   "string length",
			schema: `{"type":"string","minLength":8,"maxLength":8}`,
			assert: func(t *testing.T, value any) {
				text, ok := value.(string)
				if !ok || len([]rune(text)) != 8 {
					t.Fatalf("value=%#v", value)
				}
			},
		},
		{
			name:   "exact decimal bounds and multiple",
			schema: `{"type":"number","minimum":0.1,"maximum":0.3,"multipleOf":0.1}`,
			assert: func(t *testing.T, value any) {
				number, ok := value.(float64)
				if !ok || number != 0.3 {
					t.Fatalf("value=%#v", value)
				}
			},
		},
		{
			name:   "array minimum items",
			schema: `{"type":"array","items":{"type":"boolean"},"minItems":2,"maxItems":2}`,
			assert: func(t *testing.T, value any) {
				items, ok := value.([]any)
				if !ok || len(items) != 2 || items[0] != true || items[1] != true {
					t.Fatalf("value=%#v", value)
				}
			},
		},
		{
			name:   "required object properties",
			schema: `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"minProperties":1,"maxProperties":1,"additionalProperties":false}`,
			assert: func(t *testing.T, value any) {
				object, ok := value.(map[string]any)
				if !ok || len(object) != 1 || object["name"] != "fake" {
					t.Fatalf("value=%#v", value)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := fixtureValue(json.RawMessage(test.schema))
			if err != nil {
				t.Fatal(err)
			}
			test.assert(t, value)
		})
	}
}

func TestFixtureValueRejectsUnsupportedOrUnsatisfiableJSONSchema(t *testing.T) {
	tests := []string{
		`{"oneOf":[{"type":"string"},{"type":"integer"}]}`,
		`{"type":"string","pattern":"^[0-9]+$"}`,
		`{"$ref":"#/$defs/value","$defs":{"value":{"type":"string"}}}`,
		`{"type":"array","minItems":2,"maxItems":1}`,
		`{"type":"integer","minimum":10,"maximum":5}`,
		`{"type":"integer","minimum":9007199254740993,"maximum":9007199254740993}`,
		`{"type":"number","minimum":0.10000000000000001}`,
		`{"type":"number","const":0.10000000000000001,"maximum":0.1}`,
		`{"type":"number","const":0.30000000000000004,"multipleOf":0.1}`,
		`{"type":"array","minItems":5000}`,
		`{"type":"string","minLength":5000}`,
		`{"type":"object","minProperties":5000}`,
	}
	for _, schema := range tests {
		if _, err := fixtureValue(json.RawMessage(schema)); err == nil {
			t.Fatalf("fixtureValue(%s) succeeded", schema)
		}
	}

	largeEnum := make([]int, maxFixtureUnits+1)
	encodedEnum, err := json.Marshal(map[string]any{"type": "integer", "enum": largeEnum})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureValue(encodedEnum); err == nil {
		t.Fatal("fixtureValue accepted an enum exceeding the generation budget")
	}

	deepConst := `{"const":` + strings.Repeat(`[`, maxFixtureDepth+1) + `0` + strings.Repeat(`]`, maxFixtureDepth+1) + `}`
	if _, err := fixtureValue(json.RawMessage(deepConst)); err == nil {
		t.Fatal("fixtureValue accepted a const exceeding the nesting limit")
	}

	largeNumber := `{"const":` + strings.Repeat(`9`, maxFixtureNumberBytes+1) + `}`
	if _, err := fixtureValue(json.RawMessage(largeNumber)); err == nil {
		t.Fatal("fixtureValue accepted an oversized numeric literal")
	}
	if _, err := fixtureValue(json.RawMessage(`{"const":1e1000000}`)); err == nil {
		t.Fatal("fixtureValue accepted an oversized numeric exponent")
	}

	combinedDepth := `{"const":` + strings.Repeat(`[`, maxFixtureDepth/2+1) + `0` + strings.Repeat(`]`, maxFixtureDepth/2+1) + `}`
	for range maxFixtureDepth / 2 {
		combinedDepth = `{"type":"object","properties":{"value":` + combinedDepth + `},"required":["value"]}`
	}
	if _, err := fixtureValue(json.RawMessage(combinedDepth)); err == nil {
		t.Fatal("fixtureValue accepted combined schema and const nesting beyond the limit")
	}
}

func TestFakeStreamProducesValidSequence(t *testing.T) {
	connector := New(Options{
		ChunkSize: 3, IDGenerator: fixedIDs(uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow },
	})
	invocation := validInvocation()
	invocation.Request.Stream = true
	stream, err := connector.Stream(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	events := collectEvents(t, stream)
	if err := inference.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
	var textDeltas int
	for _, event := range events {
		if event.Type == inference.EventTextDelta {
			textDeltas++
		}
	}
	if textDeltas < 2 {
		t.Fatalf("text delta count=%d", textDeltas)
	}
}

func TestFakeStreamChunksUTF8AndToolArgumentsWithoutDataLoss(t *testing.T) {
	t.Run("utf8 text", func(t *testing.T) {
		connector := New(Options{ChunkSize: 1, IDGenerator: fixedIDs(uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow }})
		invocation := validInvocation()
		invocation.Request.Stream = true
		invocation.Request.Messages[0].Content[0].Text.Text = "你好🙂"
		stream, err := connector.Stream(context.Background(), invocation)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		var text strings.Builder
		for _, event := range collectEvents(t, stream) {
			if event.TextDelta != nil {
				if len([]rune(event.TextDelta.Text)) != 1 {
					t.Fatalf("delta=%q", event.TextDelta.Text)
				}
				text.WriteString(event.TextDelta.Text)
			}
		}
		if text.String() != "回显: 你好🙂" {
			t.Fatalf("text=%q", text.String())
		}
	})

	t.Run("tool arguments", func(t *testing.T) {
		connector := New(Options{ChunkSize: 2, IDGenerator: fixedIDs(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow }})
		invocation := validInvocation()
		invocation.Request.Stream = true
		invocation.Request.Tools = []inference.Tool{{
			Name: "weather", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}}
		stream, err := connector.Stream(context.Background(), invocation)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		var arguments strings.Builder
		events := collectEvents(t, stream)
		for _, event := range events {
			if event.ToolArgumentsDelta != nil {
				arguments.WriteString(event.ToolArgumentsDelta.Delta)
			}
		}
		if arguments.String() != `{"city":"fake"}` {
			t.Fatalf("arguments=%q", arguments.String())
		}
		if err := inference.ValidateEventSequence(events); err != nil {
			t.Fatal(err)
		}
	})
}

func TestFakeConnectorHonorsContextAndFailureInjection(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	connector := New(Options{})
	if _, err := connector.Complete(canceled, validInvocation()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete() error=%v", err)
	}
	if _, err := connector.Stream(canceled, validInvocation()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() error=%v", err)
	}
	completeErr := errors.New("injected complete failure")
	connector = New(Options{CompleteError: completeErr})
	if _, err := connector.Complete(context.Background(), validInvocation()); !errors.Is(err, completeErr) {
		t.Fatalf("Complete() error=%v", err)
	}

	startErr := errors.New("injected stream start failure")
	connector = New(Options{StreamStartError: startErr})
	if _, err := connector.Stream(context.Background(), validInvocation()); !errors.Is(err, startErr) {
		t.Fatalf("Stream() error=%v", err)
	}

	connector = New(Options{
		StreamErrorAfter: 2, StreamErrorMessage: "injected stream failure",
		IDGenerator: fixedIDs(uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow },
	})
	invocation := validInvocation()
	invocation.Request.Stream = true
	stream, err := connector.Stream(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, stream)
	if len(events) != 3 || events[2].Type != inference.EventStreamError || inference.ValidateEventSequence(events) != nil {
		t.Fatalf("injected events=%+v", events)
	}
}

func TestFakeStreamCloseIsConcurrentAndIdempotent(t *testing.T) {
	connector := New(Options{
		IDGenerator: fixedIDs(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow },
	})
	invocation := validInvocation()
	invocation.Request.Stream = true
	stream, err := connector.Stream(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := stream.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() after Close error=%v", err)
	}

	stream, err = connector.Stream(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Recv(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv() canceled error=%v", err)
	}
}

func collectEvents(t *testing.T, stream interface {
	Recv(context.Context) (inference.Event, error)
}) []inference.Event {
	t.Helper()
	var events []inference.Event
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
}

func validInvocation() gatewayport.Invocation {
	return gatewayport.Invocation{
		Request: validRequest(),
		Deployment: gatewaysnapshot.Deployment{
			ID: uuid.Must(uuid.NewV7()), ConnectorType: "fake", UpstreamModel: "fake-model",
		},
		Revision: 7,
	}
}

func validRequest() inference.Request {
	return inference.Request{
		Model: "assistant",
		Messages: []inference.Message{{Role: inference.RoleUser, Content: []inference.ContentBlock{
			{Type: inference.ContentText, Text: &inference.TextContent{Text: "hello"}},
		}}},
	}
}

func toolResultRequest() inference.Request {
	return inference.Request{
		Model: "assistant",
		Messages: []inference.Message{
			{Role: inference.RoleUser, Content: []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: "weather"}}}},
			{Role: inference.RoleAssistant, Content: []inference.ContentBlock{{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{
				ID: "call_existing", Name: "weather", Arguments: json.RawMessage(`{"city":"Shanghai"}`),
			}}}},
			{Role: inference.RoleUser, Content: []inference.ContentBlock{{Type: inference.ContentToolResult, ToolResult: &inference.ToolResultContent{
				ToolCallID: "call_existing", JSON: json.RawMessage(`{"temperature":30}`),
			}}}},
		},
	}
}

func fixedIDs(ids ...uuid.UUID) func() (uuid.UUID, error) {
	index := 0
	return func() (uuid.UUID, error) {
		if index >= len(ids) {
			return uuid.Nil, errors.New("fixed id sequence exhausted")
		}
		id := ids[index]
		index++
		return id, nil
	}
}

func TestFakeOutputDoesNotContainCredentialOrVirtualKey(t *testing.T) {
	connector := New(Options{IDGenerator: fixedIDs(uuid.Must(uuid.NewV7())), Clock: func() time.Time { return fakeNow }})
	invocation := validInvocation()
	invocation.Deployment.Credential = &gatewaysnapshot.CredentialEnvelope{Sealed: catalogSealedFixture()}
	response, err := connector.Complete(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Content[0].Text.Text, "ciphertext-secret") {
		t.Fatal("fake response leaked credential")
	}
}

func catalogSealedFixture() catalogmodel.SealedCredential {
	return catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3},
		Ciphertext: []byte("ciphertext-secret"),
	}
}
