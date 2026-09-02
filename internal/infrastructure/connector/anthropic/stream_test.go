package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

func TestMessagesStreamMapsTextToolUsageAndFinish(t *testing.T) {
	payload, err := os.Open("testdata/message_tool_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newMessagesStream(newSSEReader(payload, time.Second, 2<<20), fullInvocation(t), fixedIDGenerator, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, stream)
	if err := inference.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
	if events[0].Type != inference.EventResponseStart || events[0].ResponseStart.ID != fixedResponseID ||
		events[0].ResponseStart.Model != "assistant" || events[0].ResponseStart.CreatedAt != fixedNow.UTC() {
		t.Fatalf("start=%+v", events[0])
	}
	var arguments strings.Builder
	for _, event := range events {
		if event.Type == inference.EventToolArgumentsDelta {
			arguments.WriteString(event.ToolArgumentsDelta.Delta)
		}
	}
	if arguments.String() != `{"city":"Shanghai"}` {
		t.Fatalf("arguments=%q", arguments.String())
	}
	usage := lastUsage(t, events)
	if usage != (inference.Usage{InputTokens: 11, OutputTokens: 7, CacheReadInputTokens: 3, CacheWriteInputTokens: 2}) {
		t.Fatalf("usage=%+v", usage)
	}
	if events[len(events)-1].Type != inference.EventResponseFinish || events[len(events)-1].ResponseFinish.StopReason != inference.StopToolUse {
		t.Fatalf("finish=%+v", events[len(events)-1])
	}
}

func TestMessagesStreamIgnoresUnknownTopLevelEvent(t *testing.T) {
	body := "event: future_event\ndata: {\"type\":\"future_event\",\"opaque\":\"not-logged\"}\n\n" + minimalTextStream("end_turn")
	stream, err := newMessagesStream(newSSEReader(io.NopCloser(strings.NewReader(body)), time.Second, 2<<20), fullInvocation(t), fixedIDGenerator, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, stream)
	if err := inference.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].ResponseFinish.StopReason != inference.StopEndTurn {
		t.Fatalf("events=%+v", events)
	}
}

func TestMessagesStreamRejectsThinkingWithoutLeakingContent(t *testing.T) {
	body := messageStartEvent() +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"private-thinking\",\"signature\":\"private-signature\"}}\n\n"
	stream, err := newMessagesStream(newSSEReader(io.NopCloser(strings.NewReader(body)), time.Second, 2<<20), fullInvocation(t), fixedIDGenerator, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, _ = stream.Recv(context.Background())
	_, _ = stream.Recv(context.Background())
	_, err = stream.Recv(context.Background())
	assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
	visible := err.Error()
	if cause := errors.Unwrap(err); cause != nil {
		visible += " " + fmt.Sprint(cause)
	}
	for _, secret := range []string{"private-thinking", "private-signature"} {
		if strings.Contains(visible, secret) {
			t.Fatalf("error leaked %q: %s", secret, visible)
		}
	}
}

func TestMessagesStreamAppendsRefusalAfterFlushedText(t *testing.T) {
	payload, err := os.Open("testdata/message_refusal_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newMessagesStream(newSSEReader(payload, time.Second, 2<<20), fullInvocation(t), fixedIDGenerator, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, stream)
	if err := inference.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
	wantSuffix := []inference.EventType{
		inference.EventContentBlockStop,
		inference.EventContentBlockStart,
		inference.EventRefusalDelta,
		inference.EventContentBlockStop,
		inference.EventUsageUpdate,
		inference.EventResponseFinish,
	}
	if len(events) < len(wantSuffix) {
		t.Fatalf("events=%+v", events)
	}
	for index, want := range wantSuffix {
		if got := events[len(events)-len(wantSuffix)+index].Type; got != want {
			t.Fatalf("suffix[%d]=%s want=%s events=%+v", index, got, want, events)
		}
	}
	refusal := events[len(events)-4].RefusalDelta
	if refusal.Text != "无法处理该请求" || events[len(events)-1].ResponseFinish.StopReason != inference.StopContentFilter {
		t.Fatalf("refusal=%+v finish=%+v", refusal, events[len(events)-1])
	}
}

func TestMessagesStreamMapsUpstreamErrorToSafeTerminalEvent(t *testing.T) {
	body := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"private upstream detail\"}}\n\n"
	stream, err := newMessagesStream(newSSEReader(io.NopCloser(strings.NewReader(body)), time.Second, 2<<20), fullInvocation(t), fixedIDGenerator, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	event, err := stream.Recv(context.Background())
	if err != nil || event.Type != inference.EventStreamError || event.StreamError.Message != "供应商流式响应失败" ||
		strings.Contains(event.StreamError.Message, "private") {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF=%v", err)
	}
}

func TestMessagesStreamReturnsFirstEventBeforeUpstreamFinishes(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	release := make(chan struct{})
	go func() {
		_, _ = io.WriteString(pipeWriter, messageStartEvent())
		<-release
		_, _ = io.WriteString(pipeWriter, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		_ = pipeWriter.Close()
	}()
	stream, err := newMessagesStream(newSSEReader(pipeReader, time.Second, 2<<20), fullInvocation(t), fixedIDGenerator, fixedClock)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer stream.Close()
	result := make(chan inference.Event, 1)
	go func() {
		event, _ := stream.Recv(context.Background())
		result <- event
	}()
	select {
	case event := <-result:
		if event.Type != inference.EventResponseStart {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("first event was buffered until upstream completion")
	}
	close(release)
}

func TestMessagesStreamRejectsInvalidSequences(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "delta before start", body: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"},
		{name: "event type mismatch", body: "event: ping\ndata: {\"type\":\"message_start\"}\n\n"},
		{name: "duplicate index", body: messageStartEvent() + "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"},
		{name: "cross type delta", body: messageStartEvent() + "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n"},
		{name: "invalid tool json", body: messageStartEvent() + "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call\",\"name\":\"tool\",\"input\":{}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"},
		{name: "unknown delta", body: messageStartEvent() + "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"future_delta\",\"private\":\"detail\"}}\n\n"},
		{name: "early eof", body: messageStartEvent()},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream, err := newMessagesStream(newSSEReader(io.NopCloser(strings.NewReader(test.body)), time.Second, 2<<20), fullInvocation(t), fixedIDGenerator, fixedClock)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			err = receiveUntilError(stream)
			assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
		})
	}
}

func receiveUntilError(stream interface {
	Recv(context.Context) (inference.Event, error)
}) error {
	for index := 0; index < 20; index++ {
		_, err := stream.Recv(context.Background())
		if err != nil {
			return err
		}
	}
	return errors.New("stream did not terminate")
}

func messageStartEvent() string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
}

func minimalTextStream(reason string) string {
	return messageStartEvent() +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + reason + "\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}
