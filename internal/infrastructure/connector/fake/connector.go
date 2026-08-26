package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type Options struct {
	ChunkSize          int
	IDGenerator        func() (uuid.UUID, error)
	Clock              func() time.Time
	CompleteError      error
	StreamStartError   error
	StreamErrorAfter   int
	StreamErrorMessage string
}

type Connector struct {
	options Options
}

func New(options Options) *Connector {
	if options.ChunkSize <= 0 {
		options.ChunkSize = 8
	}
	if options.IDGenerator == nil {
		options.IDGenerator = uuid.NewV7
	}
	if options.Clock == nil {
		options.Clock = func() time.Time { return time.Now().UTC() }
	}
	if strings.TrimSpace(options.StreamErrorMessage) == "" {
		options.StreamErrorMessage = "Fake Connector 流式响应失败"
	}
	return &Connector{options: options}
}

func (c *Connector) Complete(ctx context.Context, invocation gatewayport.Invocation) (inference.Response, error) {
	if err := ctx.Err(); err != nil {
		return inference.Response{}, err
	}
	if c != nil && c.options.CompleteError != nil {
		return inference.Response{}, c.options.CompleteError
	}
	return c.buildResponse(invocation)
}

func (c *Connector) Stream(ctx context.Context, invocation gatewayport.Invocation) (inferenceport.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("fake connector is nil")
	}
	if c.options.StreamStartError != nil {
		return nil, c.options.StreamStartError
	}
	response, err := c.buildResponse(invocation)
	if err != nil {
		return nil, err
	}
	events := responseEvents(response, c.options.ChunkSize)
	return &eventStream{
		events: events, errorAfter: c.options.StreamErrorAfter, errorMessage: c.options.StreamErrorMessage,
	}, nil
}

func (c *Connector) buildResponse(invocation gatewayport.Invocation) (inference.Response, error) {
	if c == nil {
		return inference.Response{}, fmt.Errorf("fake connector is nil")
	}
	if err := invocation.Request.Validate(); err != nil {
		return inference.Response{}, fmt.Errorf("validate fake invocation: %w", err)
	}
	if invocation.Deployment.ConnectorType != "fake" {
		return inference.Response{}, fmt.Errorf("fake connector received deployment type %q", invocation.Deployment.ConnectorType)
	}
	responseID, err := c.nextID()
	if err != nil {
		return inference.Response{}, err
	}
	response := inference.Response{
		ID: responseID, Model: invocation.Request.Model, CreatedAt: c.options.Clock().UTC(),
		StopReason: inference.StopEndTurn,
	}
	if invocation.Request.MaxTokens.Set && invocation.Request.MaxTokens.Value == 0 {
		response.StopReason = inference.StopMaxTokens
		response.Usage = usageFor(invocation.Request, nil)
		return response, response.Validate()
	}

	content, reason, err := c.responseContent(invocation.Request)
	if err != nil {
		return inference.Response{}, err
	}
	response.Content = content
	response.StopReason = reason
	response.Usage = usageFor(invocation.Request, content)
	if err := response.Validate(); err != nil {
		return inference.Response{}, fmt.Errorf("validate fake response: %w", err)
	}
	return response, nil
}

func (c *Connector) responseContent(request inference.Request) ([]inference.ContentBlock, inference.StopReason, error) {
	if result := lastToolResult(request.Messages); result != nil {
		text, err := renderToolResult(result)
		if err != nil {
			return nil, "", err
		}
		return textContent("工具结果已接收: " + text), inference.StopEndTurn, nil
	}
	if tool := selectedTool(request.Tools, request.ToolChoice); tool != nil {
		arguments, err := fixtureObject(tool.InputSchema)
		if err != nil {
			return nil, "", fmt.Errorf("generate fake tool arguments: %w", err)
		}
		callID, err := c.nextID()
		if err != nil {
			return nil, "", err
		}
		return []inference.ContentBlock{{
			Type: inference.ContentToolCall,
			ToolCall: &inference.ToolCallContent{
				ID: "call_" + callID.String(), Name: tool.Name, Arguments: arguments,
			},
		}}, inference.StopToolUse, nil
	}
	if request.StructuredOutput != nil {
		value := any(map[string]any{"ok": true})
		if request.StructuredOutput.Type == inference.StructuredJSONSchema {
			var err error
			value, err = fixtureValue(request.StructuredOutput.Schema)
			if err != nil {
				return nil, "", fmt.Errorf("generate structured fake response: %w", err)
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, "", fmt.Errorf("encode structured fake response: %w", err)
		}
		return textContent(string(encoded)), inference.StopEndTurn, nil
	}
	return textContent("回显: " + renderLastUserMessage(request.Messages)), inference.StopEndTurn, nil
}

func (c *Connector) nextID() (uuid.UUID, error) {
	id, err := c.options.IDGenerator()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generate fake response id: %w", err)
	}
	if id == uuid.Nil || id.Version() != uuid.Version(7) {
		return uuid.Nil, fmt.Errorf("fake id generator must return UUIDv7")
	}
	return id, nil
}

func textContent(text string) []inference.ContentBlock {
	return []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: text}}}
}

func lastToolResult(messages []inference.Message) *inference.ToolResultContent {
	if len(messages) == 0 {
		return nil
	}
	last := messages[len(messages)-1]
	for blockIndex := len(last.Content) - 1; blockIndex >= 0; blockIndex-- {
		if result := last.Content[blockIndex].ToolResult; result != nil {
			return result
		}
	}
	return nil
}

func renderToolResult(result *inference.ToolResultContent) (string, error) {
	if result.Text != nil {
		return *result.Text, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, result.JSON); err != nil {
		return "", fmt.Errorf("compact tool result JSON: %w", err)
	}
	return compact.String(), nil
}

func selectedTool(tools []inference.Tool, choice *inference.ToolChoice) *inference.Tool {
	if len(tools) == 0 || choice != nil && choice.Mode == inference.ToolChoiceNone {
		return nil
	}
	if choice != nil && choice.Mode == inference.ToolChoiceSpecific {
		for index := range tools {
			if tools[index].Name == choice.Name {
				return &tools[index]
			}
		}
		return nil
	}
	return &tools[0]
}

func renderLastUserMessage(messages []inference.Message) string {
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		if messages[messageIndex].Role != inference.RoleUser {
			continue
		}
		var parts []string
		for _, block := range messages[messageIndex].Content {
			switch block.Type {
			case inference.ContentText:
				parts = append(parts, block.Text.Text)
			case inference.ContentImage:
				mediaType := block.Image.Source.MediaType
				if mediaType == "" {
					mediaType = "url"
				}
				parts = append(parts, "[image:"+mediaType+"]")
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	return "已收到请求"
}

func usageFor(request inference.Request, content []inference.ContentBlock) inference.Usage {
	inputLength := utf8.RuneCountInString(request.Model)
	for _, message := range request.Messages {
		for _, block := range message.Content {
			switch block.Type {
			case inference.ContentText:
				inputLength += utf8.RuneCountInString(block.Text.Text)
			case inference.ContentImage:
				inputLength += 16
			case inference.ContentToolCall:
				inputLength += utf8.RuneCountInString(block.ToolCall.Name) + len(block.ToolCall.Arguments)
			case inference.ContentToolResult:
				if block.ToolResult.Text != nil {
					inputLength += utf8.RuneCountInString(*block.ToolResult.Text)
				} else {
					inputLength += len(block.ToolResult.JSON)
				}
			}
		}
	}
	for _, tool := range request.Tools {
		inputLength += utf8.RuneCountInString(tool.Name) + len(tool.InputSchema)
	}
	outputLength := 0
	for _, block := range content {
		if block.Text != nil {
			outputLength += utf8.RuneCountInString(block.Text.Text)
		}
		if block.ToolCall != nil {
			outputLength += utf8.RuneCountInString(block.ToolCall.Name) + len(block.ToolCall.Arguments)
		}
	}
	return inference.Usage{InputTokens: tokenEstimate(inputLength), OutputTokens: tokenEstimate(outputLength)}
}

func tokenEstimate(length int) int64 {
	if length <= 0 {
		return 0
	}
	return int64((length + 3) / 4)
}

func fixtureObject(schema json.RawMessage) (json.RawMessage, error) {
	value, err := fixtureValue(schema)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("tool input schema must produce an object")
	}
	return json.Marshal(value)
}

func fixtureValue(schema json.RawMessage) (any, error) {
	var definition map[string]any
	decoder := json.NewDecoder(bytes.NewReader(schema))
	decoder.UseNumber()
	if err := decoder.Decode(&definition); err != nil || definition == nil {
		return nil, fmt.Errorf("schema must be a JSON object")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("schema must contain one JSON object")
	}
	return fixtureFromDefinition(definition)
}

func fixtureFromDefinition(definition map[string]any) (any, error) {
	generator := fixtureGenerator{remaining: maxFixtureUnits}
	return generator.generate(definition)
}

const (
	maxFixtureUnits       = 4096
	maxFixtureDepth       = 32
	maxFixtureNumberBytes = 128
	maxFixtureExponent    = 308
	maxSafeJSONInteger    = 1<<53 - 1
)

type fixtureGenerator struct {
	remaining int
	depth     int
}

func (g *fixtureGenerator) generate(definition map[string]any) (any, error) {
	if g.depth >= maxFixtureDepth {
		return nil, fmt.Errorf("JSON schema exceeds fixture nesting limit %d", maxFixtureDepth)
	}
	if err := g.consume(1); err != nil {
		return nil, err
	}
	g.depth++
	defer func() { g.depth-- }()
	if err := validateFixtureKeywords(definition); err != nil {
		return nil, err
	}
	if value, exists := definition["const"]; exists {
		if err := g.consumeValue(value); err != nil {
			return nil, err
		}
		if err := validateFixtureValue(value, definition); err != nil {
			return nil, fmt.Errorf("const does not satisfy schema: %w", err)
		}
		return value, nil
	}
	if rawValues, exists := definition["enum"]; exists {
		values, ok := rawValues.([]any)
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("enum must be a non-empty array")
		}
		if err := g.consume(len(values)); err != nil {
			return nil, fmt.Errorf("enum: %w", err)
		}
		for _, value := range values {
			if err := g.consumeValue(value); err != nil {
				return nil, fmt.Errorf("enum: %w", err)
			}
		}
		for _, value := range values {
			if validateFixtureValueIgnoringEnum(value, definition) == nil {
				return value, nil
			}
		}
		return nil, fmt.Errorf("enum contains no value satisfying the schema")
	}
	typeName, _ := definition["type"].(string)
	if _, exists := definition["type"]; exists && typeName == "" {
		return nil, fmt.Errorf("type must be a string")
	}
	if typeName == "" {
		if _, hasProperties := definition["properties"]; hasProperties {
			typeName = "object"
		}
	}
	var value any
	var err error
	switch typeName {
	case "object", "":
		value, err = g.fixtureObjectValue(definition)
	case "array":
		value, err = g.fixtureArrayValue(definition)
	case "boolean":
		value = true
	case "integer":
		value, err = fixtureNumberValue(definition, true)
	case "number":
		value, err = fixtureNumberValue(definition, false)
	case "null":
		value = nil
	case "string":
		value, err = g.fixtureStringValue(definition)
	default:
		return nil, fmt.Errorf("unsupported JSON schema type %q", typeName)
	}
	if err != nil {
		return nil, err
	}
	if err := validateFixtureValue(value, definition); err != nil {
		return nil, fmt.Errorf("generated fixture does not satisfy schema: %w", err)
	}
	return value, nil
}

func (g *fixtureGenerator) consume(units int) error {
	if units < 0 || units > g.remaining {
		return fmt.Errorf("JSON schema exceeds fixture generation budget %d", maxFixtureUnits)
	}
	g.remaining -= units
	return nil
}

func (g *fixtureGenerator) consumeValue(value any) error {
	return g.consumeValueAtDepth(value, g.depth)
}

func (g *fixtureGenerator) consumeValueAtDepth(value any, depth int) error {
	if depth >= maxFixtureDepth {
		return fmt.Errorf("fixture value exceeds nesting limit %d", maxFixtureDepth)
	}
	switch value := value.(type) {
	case string:
		return g.consume(utf8.RuneCountInString(value))
	case json.Number:
		if err := validateJSONNumberLiteral(value.String()); err != nil {
			return err
		}
		return g.consume(len(value.String()))
	case []any:
		if err := g.consume(len(value)); err != nil {
			return err
		}
		for _, item := range value {
			if err := g.consumeValueAtDepth(item, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		if err := g.consume(len(value)); err != nil {
			return err
		}
		for key, item := range value {
			if err := g.consume(utf8.RuneCountInString(key)); err != nil {
				return err
			}
			if err := g.consumeValueAtDepth(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

var supportedFixtureKeywords = map[string]struct{}{
	"$comment": {}, "$id": {}, "$schema": {},
	"additionalProperties": {}, "const": {}, "default": {}, "deprecated": {}, "description": {},
	"enum": {}, "examples": {}, "exclusiveMaximum": {}, "exclusiveMinimum": {}, "items": {},
	"maximum": {}, "maxItems": {}, "maxLength": {}, "maxProperties": {}, "minimum": {},
	"minItems": {}, "minLength": {}, "minProperties": {}, "multipleOf": {}, "properties": {},
	"readOnly": {}, "required": {}, "title": {}, "type": {}, "writeOnly": {},
}

func validateFixtureKeywords(definition map[string]any) error {
	for keyword := range definition {
		if _, supported := supportedFixtureKeywords[keyword]; !supported {
			return fmt.Errorf("unsupported JSON schema keyword %q", keyword)
		}
	}
	return nil
}

func (g *fixtureGenerator) fixtureObjectValue(definition map[string]any) (map[string]any, error) {
	properties := map[string]any{}
	if raw, exists := definition["properties"]; exists {
		var ok bool
		properties, ok = raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("properties must be an object")
		}
	}
	required, err := stringArrayKeyword(definition, "required")
	if err != nil {
		return nil, err
	}
	minProperties, err := nonNegativeIntKeyword(definition, "minProperties", 0)
	if err != nil {
		return nil, err
	}
	maxProperties, err := nonNegativeIntKeyword(definition, "maxProperties", math.MaxInt)
	if err != nil {
		return nil, err
	}
	if minProperties > maxProperties || len(required) > maxProperties {
		return nil, fmt.Errorf("object property bounds are unsatisfiable")
	}
	if minProperties > g.remaining {
		return nil, fmt.Errorf("object requires %d properties: JSON schema exceeds fixture generation budget %d", minProperties, maxFixtureUnits)
	}

	result := make(map[string]any)
	for _, key := range required {
		property, exists := properties[key]
		if !exists {
			value, err := g.fixtureAdditionalProperty(definition)
			if err != nil {
				return nil, fmt.Errorf("required property %q: %w", key, err)
			}
			result[key] = value
			continue
		}
		value, err := g.fixtureProperty(key, property)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}

	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(result) >= maxProperties {
			break
		}
		if _, exists := result[key]; exists {
			continue
		}
		value, err := g.fixtureProperty(key, properties[key])
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
	for suffix := 1; len(result) < minProperties; suffix++ {
		key := fmt.Sprintf("fake_property_%d", suffix)
		if _, exists := result[key]; exists {
			continue
		}
		value, err := g.fixtureAdditionalProperty(definition)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}

func (g *fixtureGenerator) fixtureProperty(name string, raw any) (any, error) {
	property, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("property %q schema must be an object", name)
	}
	value, err := g.generate(property)
	if err != nil {
		return nil, fmt.Errorf("property %q: %w", name, err)
	}
	return value, nil
}

func (g *fixtureGenerator) fixtureAdditionalProperty(definition map[string]any) (any, error) {
	raw, exists := definition["additionalProperties"]
	if !exists || raw == true {
		if err := g.consumeValue("fake"); err != nil {
			return nil, err
		}
		return "fake", nil
	}
	if raw == false {
		return nil, fmt.Errorf("additional properties are disabled")
	}
	schema, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("additionalProperties must be a boolean or object")
	}
	return g.generate(schema)
}

func (g *fixtureGenerator) fixtureArrayValue(definition map[string]any) ([]any, error) {
	minItems, err := nonNegativeIntKeyword(definition, "minItems", 0)
	if err != nil {
		return nil, err
	}
	maxItems, err := nonNegativeIntKeyword(definition, "maxItems", math.MaxInt)
	if err != nil {
		return nil, err
	}
	if minItems > maxItems {
		return nil, fmt.Errorf("array item bounds are unsatisfiable")
	}
	if minItems > g.remaining {
		return nil, fmt.Errorf("array requires %d items: JSON schema exceeds fixture generation budget %d", minItems, maxFixtureUnits)
	}
	result := make([]any, 0, minItems)
	if minItems == 0 {
		return result, nil
	}
	rawItems, exists := definition["items"]
	if !exists {
		for range minItems {
			if err := g.consumeValue("fake"); err != nil {
				return nil, err
			}
			result = append(result, "fake")
		}
		return result, nil
	}
	items, ok := rawItems.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("items must be an object")
	}
	for range minItems {
		value, err := g.generate(items)
		if err != nil {
			return nil, fmt.Errorf("array item: %w", err)
		}
		result = append(result, value)
	}
	return result, nil
}

func (g *fixtureGenerator) fixtureStringValue(definition map[string]any) (string, error) {
	minLength, err := nonNegativeIntKeyword(definition, "minLength", 0)
	if err != nil {
		return "", err
	}
	maxLength, err := nonNegativeIntKeyword(definition, "maxLength", math.MaxInt)
	if err != nil {
		return "", err
	}
	if minLength > maxLength {
		return "", fmt.Errorf("string length bounds are unsatisfiable")
	}
	if minLength > g.remaining {
		return "", fmt.Errorf("string requires %d runes: JSON schema exceeds fixture generation budget %d", minLength, maxFixtureUnits)
	}
	runes := []rune("fake")
	if len(runes) > maxLength {
		runes = runes[:maxLength]
	}
	for len(runes) < minLength {
		runes = append(runes, 'x')
	}
	if err := g.consume(len(runes)); err != nil {
		return "", err
	}
	return string(runes), nil
}

func fixtureNumberValue(definition map[string]any, integer bool) (any, error) {
	lower, lowerSet, err := numberKeyword(definition, "minimum")
	if err != nil {
		return nil, err
	}
	exclusiveLower, exclusiveLowerSet, err := numberKeyword(definition, "exclusiveMinimum")
	if err != nil {
		return nil, err
	}
	if exclusiveLowerSet && (!lowerSet || exclusiveLower >= lower) {
		lower, lowerSet = exclusiveLower, true
	}
	upper, upperSet, err := numberKeyword(definition, "maximum")
	if err != nil {
		return nil, err
	}
	exclusiveUpper, exclusiveUpperSet, err := numberKeyword(definition, "exclusiveMaximum")
	if err != nil {
		return nil, err
	}
	if exclusiveUpperSet && (!upperSet || exclusiveUpper <= upper) {
		upper, upperSet = exclusiveUpper, true
	}

	if integer {
		candidate := int64(1)
		if lowerSet {
			candidate = int64(math.Ceil(lower))
			if exclusiveLowerSet && float64(candidate) <= exclusiveLower {
				candidate++
			}
		}
		if upperSet && float64(candidate) > upper || exclusiveUpperSet && float64(candidate) >= exclusiveUpper {
			candidate = int64(math.Floor(upper))
			if exclusiveUpperSet && float64(candidate) >= exclusiveUpper {
				candidate--
			}
		}
		for attempts := 0; attempts < 100000; attempts++ {
			if numericValueSatisfies(float64(candidate), definition) {
				return candidate, nil
			}
			candidate++
		}
		return nil, fmt.Errorf("integer constraints are unsatisfiable or exceed fixture search limit")
	}

	candidate := 1.0
	if lowerSet && candidate < lower {
		candidate = lower
	}
	if exclusiveLowerSet && candidate <= exclusiveLower {
		candidate = math.Nextafter(exclusiveLower, math.Inf(1))
	}
	if upperSet && candidate > upper {
		candidate = upper
	}
	if exclusiveUpperSet && candidate >= exclusiveUpper {
		candidate = math.Nextafter(exclusiveUpper, math.Inf(-1))
	}
	if _, set, err := numberKeyword(definition, "multipleOf"); err != nil {
		return nil, err
	} else if set {
		candidate, err = fixtureMultipleNumber(definition, candidate)
		if err != nil {
			return nil, err
		}
	}
	if !numericValueSatisfies(candidate, definition) {
		return nil, fmt.Errorf("number constraints are unsatisfiable")
	}
	return candidate, nil
}

func fixtureMultipleNumber(definition map[string]any, candidate float64) (float64, error) {
	multiple, _, err := exactNumberKeyword(definition, "multipleOf")
	if err != nil || multiple == nil || multiple.Sign() <= 0 {
		return 0, fmt.Errorf("multipleOf must be greater than zero")
	}
	base, ok := exactRationalValue(candidate)
	if !ok {
		return 0, fmt.Errorf("number candidate cannot be represented exactly")
	}
	quotient := new(big.Rat).Quo(base, multiple)
	result := new(big.Rat).Mul(new(big.Rat).SetInt(ratCeil(quotient)), multiple)
	if encoded, ok := exactFloatFromRational(result); ok && numericValueSatisfies(encoded, definition) {
		return encoded, nil
	}

	upper, upperSet, err := exactNumberKeyword(definition, "maximum")
	if err != nil {
		return 0, err
	}
	exclusiveUpper, exclusiveUpperSet, err := exactNumberKeyword(definition, "exclusiveMaximum")
	if err != nil {
		return 0, err
	}
	if exclusiveUpperSet && (!upperSet || exclusiveUpper.Cmp(upper) <= 0) {
		upper, upperSet = exclusiveUpper, true
	}
	if upperSet {
		quotient = new(big.Rat).Quo(upper, multiple)
		multiplier := ratFloor(quotient)
		result = new(big.Rat).Mul(new(big.Rat).SetInt(multiplier), multiple)
		if exclusiveUpperSet && result.Cmp(exclusiveUpper) >= 0 {
			result.Sub(result, multiple)
		}
		if encoded, ok := exactFloatFromRational(result); ok && numericValueSatisfies(encoded, definition) {
			return encoded, nil
		}
	}
	return 0, fmt.Errorf("number constraints are unsatisfiable")
}

func ratFloor(value *big.Rat) *big.Int {
	return new(big.Int).Div(new(big.Int).Set(value.Num()), new(big.Int).Set(value.Denom()))
}

func ratCeil(value *big.Rat) *big.Int {
	negative := new(big.Rat).Neg(value)
	return new(big.Int).Neg(ratFloor(negative))
}

func exactFloatFromRational(value *big.Rat) (float64, bool) {
	encoded, _ := value.Float64()
	roundTrip, ok := exactRationalValue(encoded)
	return encoded, ok && roundTrip.Cmp(value) == 0
}

func validateFixtureValue(value any, definition map[string]any) error {
	return validateFixtureValueWithEnum(value, definition, true)
}

func validateFixtureValueIgnoringEnum(value any, definition map[string]any) error {
	return validateFixtureValueWithEnum(value, definition, false)
}

func validateFixtureValueWithEnum(value any, definition map[string]any, checkEnum bool) error {
	if err := validateFixtureKeywords(definition); err != nil {
		return err
	}
	if expected, exists := definition["const"]; exists && !reflect.DeepEqual(value, expected) {
		return fmt.Errorf("value does not match const")
	}
	if rawEnum, exists := definition["enum"]; checkEnum && exists {
		values, ok := rawEnum.([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("enum must be a non-empty array")
		}
		matched := false
		for _, candidate := range values {
			if reflect.DeepEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("value is not in enum")
		}
	}
	typeName, _ := definition["type"].(string)
	switch typeName {
	case "", "null", "boolean", "string", "integer", "number", "array", "object":
	default:
		return fmt.Errorf("unsupported JSON schema type %q", typeName)
	}
	if typeName == "null" && value != nil {
		return fmt.Errorf("value is not null")
	}
	if typeName == "boolean" {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("value is not a boolean")
		}
	}
	if text, ok := value.(string); ok {
		minLength, err := nonNegativeIntKeyword(definition, "minLength", 0)
		if err != nil {
			return err
		}
		maxLength, err := nonNegativeIntKeyword(definition, "maxLength", math.MaxInt)
		if err != nil {
			return err
		}
		length := utf8.RuneCountInString(text)
		if length < minLength || length > maxLength {
			return fmt.Errorf("string length is outside bounds")
		}
	} else if typeName == "string" {
		return fmt.Errorf("value is not a string")
	}
	if number, ok := exactRationalValue(value); ok {
		if typeName == "integer" && !number.IsInt() {
			return fmt.Errorf("value is not an integer")
		}
		if !numericValueSatisfies(value, definition) {
			return fmt.Errorf("number is outside constraints")
		}
	} else if typeName == "number" || typeName == "integer" {
		return fmt.Errorf("value is not a number")
	}
	if items, ok := value.([]any); ok {
		minItems, err := nonNegativeIntKeyword(definition, "minItems", 0)
		if err != nil {
			return err
		}
		maxItems, err := nonNegativeIntKeyword(definition, "maxItems", math.MaxInt)
		if err != nil {
			return err
		}
		if len(items) < minItems || len(items) > maxItems {
			return fmt.Errorf("array length is outside bounds")
		}
		if rawItems, exists := definition["items"]; exists {
			itemSchema, ok := rawItems.(map[string]any)
			if !ok {
				return fmt.Errorf("items must be an object")
			}
			for _, item := range items {
				if err := validateFixtureValue(item, itemSchema); err != nil {
					return err
				}
			}
		}
	} else if typeName == "array" {
		return fmt.Errorf("value is not an array")
	}
	if object, ok := value.(map[string]any); ok {
		return validateFixtureObject(object, definition)
	} else if typeName == "object" {
		return fmt.Errorf("value is not an object")
	}
	return nil
}

func validateFixtureObject(object map[string]any, definition map[string]any) error {
	minProperties, err := nonNegativeIntKeyword(definition, "minProperties", 0)
	if err != nil {
		return err
	}
	maxProperties, err := nonNegativeIntKeyword(definition, "maxProperties", math.MaxInt)
	if err != nil {
		return err
	}
	if len(object) < minProperties || len(object) > maxProperties {
		return fmt.Errorf("object property count is outside bounds")
	}
	required, err := stringArrayKeyword(definition, "required")
	if err != nil {
		return err
	}
	for _, key := range required {
		if _, exists := object[key]; !exists {
			return fmt.Errorf("required property %q is missing", key)
		}
	}
	properties, _ := definition["properties"].(map[string]any)
	for key, value := range object {
		if rawProperty, exists := properties[key]; exists {
			property, ok := rawProperty.(map[string]any)
			if !ok {
				return fmt.Errorf("property %q schema must be an object", key)
			}
			if err := validateFixtureValue(value, property); err != nil {
				return fmt.Errorf("property %q: %w", key, err)
			}
			continue
		}
		if raw, exists := definition["additionalProperties"]; exists {
			if raw == false {
				return fmt.Errorf("additional property %q is disabled", key)
			}
			if schema, ok := raw.(map[string]any); ok {
				if err := validateFixtureValue(value, schema); err != nil {
					return fmt.Errorf("additional property %q: %w", key, err)
				}
			}
		}
	}
	return nil
}

func numericValueSatisfies(value any, definition map[string]any) bool {
	number, ok := exactRationalValue(value)
	if !ok {
		return false
	}
	if minimum, set, err := exactNumberKeyword(definition, "minimum"); err != nil || set && number.Cmp(minimum) < 0 {
		return false
	}
	if minimum, set, err := exactNumberKeyword(definition, "exclusiveMinimum"); err != nil || set && number.Cmp(minimum) <= 0 {
		return false
	}
	if maximum, set, err := exactNumberKeyword(definition, "maximum"); err != nil || set && number.Cmp(maximum) > 0 {
		return false
	}
	if maximum, set, err := exactNumberKeyword(definition, "exclusiveMaximum"); err != nil || set && number.Cmp(maximum) >= 0 {
		return false
	}
	if multiple, set, err := exactNumberKeyword(definition, "multipleOf"); err != nil || set && (multiple.Sign() <= 0 || !new(big.Rat).Quo(number, multiple).IsInt()) {
		return false
	}
	return true
}

func exactRationalValue(value any) (*big.Rat, bool) {
	var encoded string
	switch number := value.(type) {
	case *big.Rat:
		if number == nil {
			return nil, false
		}
		return new(big.Rat).Set(number), true
	case json.Number:
		encoded = number.String()
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, false
		}
		encoded = strconv.FormatFloat(number, 'g', -1, 64)
	case int64:
		encoded = strconv.FormatInt(number, 10)
	case int:
		encoded = strconv.Itoa(number)
	default:
		return nil, false
	}
	if validateJSONNumberLiteral(encoded) != nil {
		return nil, false
	}
	rational, ok := new(big.Rat).SetString(encoded)
	return rational, ok
}

func validateJSONNumberLiteral(encoded string) error {
	if encoded == "" || len(encoded) > maxFixtureNumberBytes {
		return fmt.Errorf("JSON number literal exceeds %d bytes", maxFixtureNumberBytes)
	}
	mantissa := encoded
	if exponentIndex := strings.IndexAny(encoded, "eE"); exponentIndex >= 0 {
		mantissa = encoded[:exponentIndex]
		exponent, err := strconv.ParseInt(encoded[exponentIndex+1:], 10, 32)
		if err != nil || exponent < -maxFixtureExponent || exponent > maxFixtureExponent {
			return fmt.Errorf("JSON number exponent exceeds supported range +/-%d", maxFixtureExponent)
		}
	}
	digits := 0
	for _, character := range mantissa {
		if character >= '0' && character <= '9' {
			digits++
		}
	}
	if digits == 0 || digits > maxFixtureNumberBytes {
		return fmt.Errorf("JSON number has unsupported precision")
	}
	return nil
}

func exactNumberKeyword(definition map[string]any, keyword string) (*big.Rat, bool, error) {
	raw, exists := definition[keyword]
	if !exists {
		return nil, false, nil
	}
	value, ok := exactRationalValue(raw)
	if !ok {
		return nil, false, fmt.Errorf("%s must be a finite JSON number", keyword)
	}
	return value, true, nil
}

func numericValue(value any) (float64, bool) {
	rational, ok := exactRationalValue(value)
	if !ok {
		return 0, false
	}
	limit := new(big.Rat).SetInt64(maxSafeJSONInteger)
	if new(big.Rat).Abs(rational).Cmp(limit) > 0 {
		return 0, false
	}
	parsed, _ := rational.Float64()
	roundTrip, ok := exactRationalValue(parsed)
	if !ok || roundTrip.Cmp(rational) != 0 {
		return 0, false
	}
	return parsed, true
}

func numberKeyword(definition map[string]any, keyword string) (float64, bool, error) {
	raw, exists := definition[keyword]
	if !exists {
		return 0, false, nil
	}
	value, ok := numericValue(raw)
	if !ok {
		return 0, false, fmt.Errorf("%s must be a finite number within the exact fixture range", keyword)
	}
	return value, true, nil
}

func nonNegativeIntKeyword(definition map[string]any, keyword string, fallback int) (int, error) {
	value, exists, err := numberKeyword(definition, keyword)
	if err != nil {
		return 0, err
	}
	if !exists {
		return fallback, nil
	}
	if value < 0 || math.Trunc(value) != value || value > float64(math.MaxInt) {
		return 0, fmt.Errorf("%s must be a non-negative integer", keyword)
	}
	return int(value), nil
}

func stringArrayKeyword(definition map[string]any, keyword string) ([]string, error) {
	raw, exists := definition[keyword]
	if !exists {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", keyword)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok || value == "" {
			return nil, fmt.Errorf("%s must contain non-empty strings", keyword)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate value %q", keyword, value)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

var _ gatewayport.Connector = (*Connector)(nil)
