package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

type decisionQuestion struct {
	Type    string
	Options map[string]json.RawMessage
	Levels  []json.RawMessage
}

func numberInRange(raw []byte, minimum, maximum float64) bool {
	var value *float64
	return json.Unmarshal(raw, &value) == nil && value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= minimum && *value <= maximum
}

func validateDecisionResponse(raw []byte, questions map[string]decisionQuestion) error {
	invalid := pluginabi.NewError("invalid_upstream_response", "upstream returned an invalid JEV decision response", 502)
	payload, err := decodeObject(raw)
	if err != nil {
		return invalid
	}
	var model string
	if json.Unmarshal(payload["model"], &model) != nil || model == "" {
		return invalid
	}
	usage, err := decodeObject(payload["usage"])
	if err != nil {
		return invalid
	}
	for _, field := range []string{"input_tokens", "output_tokens"} {
		var tokens *int64
		if json.Unmarshal(usage[field], &tokens) != nil || tokens == nil || *tokens < 0 {
			return invalid
		}
	}
	answers, err := decodeObject(payload["answers"])
	if err != nil || len(answers) != len(questions) {
		return invalid
	}
	for id, question := range questions {
		answer, err := decodeObject(answers[id])
		if err != nil {
			return invalid
		}
		var kind string
		if json.Unmarshal(answer["type"], &kind) != nil || kind != question.Type {
			return invalid
		}
		switch kind {
		case "noul":
			if !numberInRange(answer["noul"], 0, 1) {
				return invalid
			}
		case "choice", "score":
			if !numberInRange(answer["confidence"], 0, 1) {
				return invalid
			}
			probabilities, err := decodeObject(answer["probabilities"])
			if err != nil {
				return invalid
			}
			expected := len(question.Options)
			if kind == "score" {
				expected = len(question.Levels)
			}
			if len(probabilities) != expected {
				return invalid
			}
			for option, probability := range probabilities {
				if !numberInRange(probability, 0, 1) {
					return invalid
				}
				if kind == "choice" {
					if _, ok := question.Options[option]; !ok {
						return invalid
					}
				} else {
					index, err := strconv.Atoi(option)
					if err != nil || index < 0 || index >= expected || strconv.Itoa(index) != option {
						return invalid
					}
				}
			}
			if kind == "choice" {
				var choice string
				if json.Unmarshal(answer["choice"], &choice) != nil {
					return invalid
				}
				if _, ok := question.Options[choice]; !ok {
					return invalid
				}
			} else if !numberInRange(answer["score"], 0, float64(expected-1)) {
				return invalid
			}
		}
	}
	return nil
}

// Decode fields as raw JSON so structured caller data and large numbers stay intact.
func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("expected JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid object key")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("duplicate object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid JSON object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected trailing JSON")
	}
	return fields, nil
}

func structuredValue(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '"' || raw[0] == '{' || raw[0] == '[')
}

func readDecision(raw []byte) (map[string]json.RawMessage, map[string]decisionQuestion, error) {
	invalid := pluginabi.NewError("invalid_decision", "expected native JEV state and typed questions with valid instructions and criteria", 400)
	payload, err := decodeObject(raw)
	if err != nil || !structuredValue(payload["state"]) {
		return nil, nil, invalid
	}
	for _, field := range []string{"messages", "tools", "tool_choice", "input", "instructions"} {
		if _, exists := payload[field]; exists {
			return nil, nil, pluginabi.NewError("unsupported_chat", "JEV accepts decisions, not chat or tool requests", 400)
		}
	}
	if value, exists := payload["stream"]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("false")) {
		return nil, nil, pluginabi.NewError("unsupported_stream", "JEV does not support streaming", 400)
	}
	questions, err := decodeObject(payload["questions"])
	if err != nil || len(questions) == 0 {
		return nil, nil, invalid
	}
	parsed := make(map[string]decisionQuestion, len(questions))
	for id, rawQuestion := range questions {
		fields, err := decodeObject(rawQuestion)
		if err != nil || !structuredValue(fields["instructions"]) {
			return nil, nil, invalid
		}
		var question decisionQuestion
		if err := json.Unmarshal(fields["type"], &question.Type); err != nil {
			return nil, nil, invalid
		}
		switch question.Type {
		case "noul":
			if criteria, exists := fields["criteria"]; exists {
				options, err := decodeObject(criteria)
				if err != nil {
					return nil, nil, invalid
				}
				for name, value := range options {
					if (name != "true" && name != "false") || !structuredValue(value) {
						return nil, nil, invalid
					}
				}
			}
		case "choice":
			question.Options, err = decodeObject(fields["criteria"])
			if err != nil || len(question.Options) == 0 || len(question.Options) > 255 {
				return nil, nil, invalid
			}
			for _, value := range question.Options {
				if !structuredValue(value) && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return nil, nil, invalid
				}
			}
		case "score":
			if err := json.Unmarshal(fields["criteria"], &question.Levels); err != nil || len(question.Levels) < 2 || len(question.Levels) > 10 {
				return nil, nil, invalid
			}
			for _, value := range question.Levels {
				if !structuredValue(value) {
					return nil, nil, invalid
				}
			}
		default:
			return nil, nil, invalid
		}
		parsed[id] = question
	}
	return payload, parsed, nil
}
